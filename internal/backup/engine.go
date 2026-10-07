package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Config struct {
	Root, StatePath, RemoteParent, RemoteName string
	Concurrency, MaxAttempts                  int
	PollInterval, RetryBase                   time.Duration
	PartBytes                                 int64
	RetryFailed                               bool
	Log                                       func(Event)
}
type Event struct {
	Path, Status string
	Attempts     int
}
type Summary struct{ Found, Uploaded, Skipped, Failed, Paused int }
type Entry struct {
	Hash       string `json:"sha256"`
	Size       int64  `json:"size"`
	Key        string `json:"request_key"`
	FolderID   string `json:"folder_id"`
	RemoteName string `json:"remote_name"`
	SessionID  string `json:"session_id,omitempty"`
	CancelID   string `json:"cancel_session_id,omitempty"`
	FileID     string `json:"file_id,omitempty"`
	Done       bool   `json:"done"`
	Attempts   int    `json:"attempts"`
	NextTry    int64  `json:"next_try_ms"`
}
type state struct {
	Version   int              `json:"version"`
	Endpoint  string           `json:"endpoint"`
	UserID    int64            `json:"user_id"`
	Root      string           `json:"root"`
	Parent    string           `json:"remote_parent"`
	Name      string           `json:"remote_name"`
	PartBytes int64            `json:"part_bytes"`
	Entries   map[string]Entry `json:"entries"`
}
type Engine struct {
	cfg               Config
	client            *Client
	root              *os.Root
	unlock            func()
	mu, cycle, dirsMu sync.Mutex
	st                state
	dirs              map[string]string
	stateErr          error
}
type source struct {
	rel, hash string
	size      int64
}
type stateError struct{ err error }

func (e *stateError) Error() string {
	return "backup state could not be saved; stopped to preserve restart safety"
}
func (e *stateError) Unwrap() error { return e.err }

func sum(data string) string { h := sha256.Sum256([]byte(data)); return hex.EncodeToString(h[:]) }
func newKey() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func cleanName(s string) string {
	original := s
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		s = "item"
	}
	r := []rune(s)
	if s != original || len(r) > 180 {
		if len(r) > 180 {
			r = r[:180]
		}
		return string(r) + " [" + sum(original) + "]"
	}
	return s
}
func versionName(rel, hash string) string {
	r := []rune(cleanName(path.Base(rel)))
	if len(r) > 120 {
		r = r[:120]
	}
	// Both full digests avoid collisions from truncated names and preserve the
	// version identity even after a server receipt expires or local state is lost.
	return string(r) + " [" + sum(rel) + "-" + hash + "]"
}
func within(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func New(cfg Config, c *Client) (*Engine, error) {
	if c == nil || c.UserID <= 0 || cfg.Root == "" || cfg.StatePath == "" {
		return nil, errors.New("authenticated client, explicit root and state path are required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source must be an existing directory, not a symlink")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(root) == root {
		return nil, errors.New("refusing to scan an entire filesystem")
	}
	cfg.Root = root
	cfg.StatePath, err = filepath.Abs(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(cfg.StatePath), 0700); err != nil {
		return nil, err
	}
	stateDir, err := filepath.EvalSymlinks(filepath.Dir(cfg.StatePath))
	if err != nil {
		return nil, err
	}
	cfg.StatePath = filepath.Join(stateDir, filepath.Base(cfg.StatePath))
	if within(root, stateDir) {
		return nil, errors.New("state directory must be outside the source directory")
	}
	if cfg.RemoteParent != "" && !idPattern.MatchString(cfg.RemoteParent) {
		return nil, errors.New("invalid remote parent ID")
	}
	if cfg.RemoteName == "" {
		base := []rune(cleanName(filepath.Base(root)))
		if len(base) > 120 {
			base = base[:120]
		}
		cfg.RemoteName = string(base) + "-backup-" + sum(root)[:12]
	}
	if cfg.RemoteName != cleanName(cfg.RemoteName) {
		return nil, errors.New("invalid remote folder name")
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 2
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = 30 * time.Second
	}
	if cfg.PartBytes == 0 {
		cfg.PartBytes = 8 << 20
	}
	if cfg.Concurrency < 1 || cfg.Concurrency > 8 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 || cfg.PollInterval < time.Millisecond || cfg.RetryBase < time.Millisecond || cfg.PartBytes < 1 || cfg.PartBytes > 8<<20 {
		return nil, errors.New("invalid concurrency, retry, interval or part size")
	}
	unlock, err := lockState(cfg.StatePath + ".lock")
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, client: c, unlock: unlock, dirs: make(map[string]string)}
	e.root, err = os.OpenRoot(root) // Kernel-enforced containment even during symlink/junction replacement.
	if err != nil {
		unlock()
		return nil, err
	}
	fail := func(err error) (*Engine, error) { e.Close(); return nil, err }
	e.st = state{Version: 1, Endpoint: c.Endpoint, UserID: c.UserID, Root: root, Parent: cfg.RemoteParent, Name: cfg.RemoteName, PartBytes: cfg.PartBytes, Entries: make(map[string]Entry)}
	if info, err := os.Lstat(cfg.StatePath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return fail(errors.New("invalid backup state file"))
		}
		data, err := os.ReadFile(cfg.StatePath)
		if err != nil {
			return fail(err)
		}
		var previous state
		if err = json.Unmarshal(data, &previous); err != nil {
			return fail(errors.New("corrupt backup state; retain it and use another state path only after review"))
		}
		if previous.Version != 1 || previous.Endpoint != e.st.Endpoint || previous.UserID != e.st.UserID || previous.Root != root || previous.Parent != cfg.RemoteParent || previous.Name != cfg.RemoteName || previous.PartBytes != cfg.PartBytes || previous.Entries == nil {
			return fail(errors.New("state belongs to another source, server, account, target or part size"))
		}
		for rel, entry := range previous.Entries {
			if !fs.ValidPath(rel) || entry.Size < 0 || !hashPattern.MatchString(entry.Hash) || entry.RemoteName != versionName(rel, entry.Hash) || !idPattern.MatchString(entry.Key) || entry.Attempts < 0 || entry.SessionID != "" && !idPattern.MatchString(entry.SessionID) || entry.CancelID != "" && !idPattern.MatchString(entry.CancelID) || entry.FolderID != "" && !idPattern.MatchString(entry.FolderID) || entry.FileID != "" && !idPattern.MatchString(entry.FileID) {
				return fail(errors.New("invalid backup state entry"))
			}
			if cfg.RetryFailed && !entry.Done {
				entry.Attempts = 0
				entry.NextTry = 0
				previous.Entries[rel] = entry
			}
		}
		e.st = previous
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err = e.saveLocked(); err != nil {
		return fail(err)
	}
	return e, nil
}
func (e *Engine) Close() {
	if e.root != nil {
		e.root.Close()
		e.root = nil
	}
	if e.unlock != nil {
		e.unlock()
		e.unlock = nil
	}
}
func (e *Engine) saveLocked() error {
	if e.stateErr != nil {
		return e.stateErr
	}
	data, err := json.MarshalIndent(e.st, "", "  ")
	if err == nil {
		var tmp *os.File
		tmp, err = os.CreateTemp(filepath.Dir(e.cfg.StatePath), ".netdisk-sync-state-")
		if err == nil {
			name := tmp.Name()
			defer os.Remove(name) // Only our generated temporary state file.
			if _, err = tmp.Write(data); err == nil {
				err = tmp.Sync()
			}
			closeErr := tmp.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(name, e.cfg.StatePath)
			}
			if err == nil {
				if dir, openErr := os.Open(filepath.Dir(e.cfg.StatePath)); openErr == nil {
					_ = dir.Sync()
					dir.Close()
				}
			}
		}
	}
	if err != nil {
		e.stateErr = &stateError{err}
		return e.stateErr
	}
	return nil
}
func (e *Engine) update(rel string, fn func(*Entry)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateErr != nil {
		return e.stateErr
	}
	v := e.st.Entries[rel]
	fn(&v)
	e.st.Entries[rel] = v
	return e.saveLocked()
}
func (e *Engine) entry(rel string) Entry { e.mu.Lock(); defer e.mu.Unlock(); return e.st.Entries[rel] }
func (e *Engine) log(rel, status string, attempts int) {
	if e.cfg.Log != nil {
		e.cfg.Log(Event{rel, status, attempts})
	}
}
func (e *Engine) scan(ctx context.Context) ([]source, []error) {
	var files []source
	var problems []error
	err := fs.WalkDir(e.root.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			problems = append(problems, fmt.Errorf("scan %q: %w", rel, walkErr))
			return nil
		}
		if rel == "." || d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			e.log(rel, "symlink skipped", 0)
			return nil
		}
		if !d.Type().IsRegular() {
			e.log(rel, "nonregular skipped", 0)
			return nil
		}
		f, err := e.root.Open(rel)
		if err != nil {
			problems = append(problems, fmt.Errorf("open %q: %w", rel, err))
			return nil
		}
		defer f.Close()
		before, err := f.Stat()
		if err != nil || !before.Mode().IsRegular() {
			e.log(rel, "nonregular skipped", 0)
			return nil
		}
		if before.Size() > e.client.MaxUploadBytes {
			problems = append(problems, fmt.Errorf("%q: exceeds server file limit", rel))
			return nil
		}
		h := sha256.New()
		n, err := io.CopyBuffer(h, io.LimitReader(f, before.Size()+1), make([]byte, 128<<10))
		after, statErr := f.Stat()
		if err != nil || statErr != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			problems = append(problems, fmt.Errorf("%q: source changed or could not be read; deferred", rel))
			return nil
		}
		files = append(files, source{rel, hex.EncodeToString(h.Sum(nil)), n})
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}
	return files, problems
}
func (e *Engine) folder(ctx context.Context, rel string) (string, error) {
	e.dirsMu.Lock()
	defer e.dirsMu.Unlock()
	parent, ok := e.dirs["."]
	if !ok {
		var err error
		parent, err = e.client.ensureFolder(ctx, e.cfg.RemoteParent, e.cfg.RemoteName)
		if err != nil {
			return "", err
		}
		e.dirs["."] = parent
	}
	current := ""
	dir := path.Dir(rel)
	if dir == "." {
		return parent, nil
	}
	for _, part := range strings.Split(dir, "/") {
		if current != "" {
			current += "/"
		}
		current += part
		if cached, ok := e.dirs[current]; ok {
			parent = cached
			continue
		}
		id, err := e.client.ensureFolder(ctx, parent, cleanName(part))
		if err != nil {
			return "", err
		}
		e.dirs[current] = id
		parent = id
	}
	return parent, nil
}
func (e *Engine) reconcile(ctx context.Context, v Entry) (string, error) {
	_, files, err := e.client.directory(ctx, v.FolderID)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.Name != v.RemoteName {
			continue
		}
		if f.Size != v.Size {
			return "", errors.New("remote version name conflict")
		}
		if err = e.client.verify(ctx, f, v.Hash); err != nil {
			return "", err
		}
		return f.ID, nil
	}
	return "", nil
}
func (e *Engine) upload(ctx context.Context, s source) error {
	v := e.entry(s.rel)
	if v.CancelID != "" {
		if err := e.client.cancel(ctx, v.CancelID); err != nil {
			return err
		}
		if err := e.update(s.rel, func(v *Entry) { v.CancelID = "" }); err != nil {
			return err
		}
	}
	folder, err := e.folder(ctx, s.rel)
	if err != nil {
		return err
	}
	if err = e.update(s.rel, func(v *Entry) { v.FolderID = folder }); err != nil {
		return err
	}
	v = e.entry(s.rel)
	// Listing + streamed hash reconciliation handles completion-response loss,
	// expired receipts and even a reviewed recovery with a new local state file.
	existing, err := e.reconcile(ctx, v)
	if err != nil {
		return err
	}
	if existing != "" {
		return e.update(s.rel, func(v *Entry) { v.Done = true; v.FileID = existing; v.SessionID = "" })
	}
	session, err := e.client.create(ctx, v, e.cfg.PartBytes)
	if err != nil {
		return err
	}
	if err = e.update(s.rel, func(v *Entry) { v.SessionID = session.ID }); err != nil {
		return err
	}
	if session.File != nil {
		if session.File.Name != v.RemoteName || session.File.Size != v.Size {
			return errors.New("invalid completion receipt")
		}
		if err = e.client.verify(ctx, *session.File, v.Hash); err != nil {
			return err
		}
		return e.update(s.rel, func(v *Entry) { v.Done = true; v.FileID = session.File.ID; v.SessionID = "" })
	}
	parts, err := e.client.parts(ctx, session.ID)
	if err != nil {
		return err
	}
	existingParts := make(map[int64]Part)
	for _, p := range parts {
		existingParts[p.Index] = p
	}
	f, err := e.root.Open(s.rel)
	if err != nil {
		return errors.New("source unavailable; deferred")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != s.size {
		return errors.New("source changed; deferred")
	}
	h := sha256.New()
	buf := make([]byte, e.cfg.PartBytes)
	for index, offset := int64(0), int64(0); offset < s.size; index++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n := e.cfg.PartBytes
		if s.size-offset < n {
			n = s.size - offset
		}
		chunk := buf[:n]
		if _, err = io.ReadFull(io.NewSectionReader(f, offset, n), chunk); err != nil {
			return errors.New("source read failed; deferred")
		}
		h.Write(chunk)
		digest := sha256.Sum256(chunk)
		hash := hex.EncodeToString(digest[:])
		if p, ok := existingParts[index]; ok {
			if p.Size != n || p.Hash != hash {
				return errors.New("source differs from saved upload parts; deferred")
			}
		} else {
			if err = e.client.request(ctx, "PUT", fmt.Sprintf("/api/uploads/%s/parts/%d", session.ID, index), chunk, nil); err != nil {
				return err
			}
		}
		offset += n
	}
	if hex.EncodeToString(h.Sum(nil)) != s.hash {
		return errors.New("source changed during upload; version not published")
	}
	var result File
	if err = e.client.request(ctx, "POST", "/api/uploads/"+session.ID+"/complete", nil, &result); err != nil {
		return err
	}
	if !idPattern.MatchString(result.ID) || result.Name != v.RemoteName || result.Size != v.Size {
		return errors.New("invalid completion response")
	}
	return e.update(s.rel, func(v *Entry) { v.Done = true; v.FileID = result.ID; v.SessionID = "" })
}
func (e *Engine) process(ctx context.Context, s source) (string, error) {
	old := e.entry(s.rel)
	if old.Hash == s.hash && old.Done {
		return "skipped", nil
	}
	if old.Hash != s.hash {
		// Cancellation is persisted and consumes the new version attempt budget.
		// Only our superseded upload session is cancelled, never a cloud file.
		cancelID := old.SessionID
		if cancelID == "" {
			cancelID = old.CancelID
		}
		key, err := newKey()
		if err != nil {
			return "failed", err
		}
		if err = e.update(s.rel, func(v *Entry) {
			*v = Entry{Hash: s.hash, Size: s.size, Key: key, RemoteName: versionName(s.rel, s.hash), CancelID: cancelID}
		}); err != nil {
			return "failed", err
		}
	}
	v := e.entry(s.rel)
	if v.Attempts >= e.cfg.MaxAttempts {
		return "paused", nil
	}
	if time.Now().UnixMilli() < v.NextTry {
		return "waiting", nil
	}
	// Persist the attempt before any upload mutation, including a process crash.
	if err := e.update(s.rel, func(v *Entry) { v.Attempts++ }); err != nil {
		return "failed", err
	}
	err := e.upload(ctx, s)
	v = e.entry(s.rel)
	if err == nil {
		e.log(s.rel, "backed up", v.Attempts)
		return "uploaded", nil
	}
	delay := e.cfg.RetryBase
	for n := 1; n < v.Attempts && delay < 15*time.Minute; n++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	if saveErr := e.update(s.rel, func(v *Entry) { v.NextTry = time.Now().Add(delay).UnixMilli() }); saveErr != nil {
		return "failed", saveErr
	}
	e.log(s.rel, fmt.Sprintf("failed (%v); attempt %d/%d", err, v.Attempts, e.cfg.MaxAttempts), v.Attempts)
	return "failed", err
}
func (e *Engine) SyncOnce(ctx context.Context) (Summary, error) {
	e.cycle.Lock()
	defer e.cycle.Unlock()
	sources, problems := e.scan(ctx)
	result := Summary{Found: len(sources), Failed: len(problems)}
	e.dirsMu.Lock()
	e.dirs = make(map[string]string)
	e.dirsMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan source)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for i := 0; i < e.cfg.Concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for s := range jobs {
				status, err := e.process(ctx, s)
				mu.Lock()
				switch status {
				case "uploaded":
					result.Uploaded++
				case "skipped":
					result.Skipped++
				case "paused", "waiting":
					result.Paused++
				default:
					result.Failed++
				}
				if err != nil {
					problems = append(problems, fmt.Errorf("%q: %w", s.rel, err))
				}
				mu.Unlock()
				var fatal *stateError
				if errors.As(err, &fatal) {
					cancel()
				}
			}
		}()
	}
	for _, s := range sources {
		if ctx.Err() != nil {
			break
		}
		jobs <- s
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		problems = append(problems, ctx.Err())
	}
	return result, errors.Join(problems...)
}
func (e *Engine) Run(ctx context.Context) error {
	timer := time.NewTicker(e.cfg.PollInterval)
	defer timer.Stop()
	for {
		_, err := e.SyncOnce(ctx)
		var fatal *stateError
		if errors.As(err, &fatal) {
			return err
		}
		if err != nil {
			e.log("", "cycle incomplete; failures retained for bounded retry", 0)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
