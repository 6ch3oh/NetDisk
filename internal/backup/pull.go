package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const diskMarker = ".netdisk-disk.json"
const diskManifest = ".netdisk-manifest.json"

type PullConfig struct {
	Root, StatePath, DiskLabel       string
	InitDisk, AdoptDisk, RetryFailed bool
	PartBytes, ReserveBytes          int64
	MaxAttempts                      int
	PollInterval, RetryBase          time.Duration
	Log                              func(Event)
	RestoreToOtherServer             bool
}
type DiskBinding struct {
	Version   int    `json:"version"`
	DiskID    string `json:"disk_id"`
	Label     string `json:"label"`
	Endpoint  string `json:"endpoint"`
	UserID    int64  `json:"user_id"`
	PartBytes int64  `json:"part_bytes"`
}
type PullEntry struct {
	File           RemoteFile `json:"file"`
	LocalPath      string     `json:"local_path"`
	Parts          []string   `json:"verified_part_hashes,omitempty"`
	Done           bool       `json:"verified"`
	Attempts       int        `json:"attempts"`
	NextTry        int64      `json:"next_try_ms"`
	ArchiveID      string     `json:"archive_id,omitempty"`
	ArchivedAt     int64      `json:"archived_at,omitempty"`
	RestoreKey     string     `json:"restore_key,omitempty"`
	RestoredFileID string     `json:"restored_file_id,omitempty"`
	RestoreTarget  string     `json:"restore_target,omitempty"`
}
type PullState struct {
	Binding DiskBinding             `json:"binding"`
	Entries map[string]PullEntry    `json:"versions"`
	Latest  map[string]string       `json:"latest"`
	Folders map[string]RemoteFolder `json:"folders"`
}
type PullSummary struct{ Found, Downloaded, Skipped, Failed, Paused int }
type PullEngine struct {
	cfg     PullConfig
	client  *Client
	binding DiskBinding
	unlock  func()
	mu      sync.Mutex
}

func portableName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	// Prefixing also avoids Windows device names such as CON and COM1.
	for len(name) > 120 {
		r := []rune(name)
		name = string(r[:len(r)-1])
	}
	if name == "" {
		name = "file"
	}
	return "file-" + name
}
func localVersion(f RemoteFile) string {
	return "versions/" + f.ID + "/" + f.SHA256 + "/" + portableName(f.Name)
}
func readRegular(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := openRegular(root, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > max {
		return nil, errors.New("backup metadata exceeds size limit")
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}
func openRegular(root *os.Root, name string, flags int) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("backup path is not a regular file")
	}
	f, err := root.OpenFile(name, flags, 0600)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, errors.New("backup path changed")
	}
	return f, nil
}
func writeDiskJSON(root *os.Root, name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	id, err := newKey()
	if err != nil {
		return err
	}
	tmp := ".netdisk-write-" + id
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err == nil {
		if d, e := root.Open("."); e == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	return err
}
func saveBinding(path string, binding DiskBinding) error {
	b, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func NewPull(cfg PullConfig, c *Client) (*PullEngine, error) {
	if c == nil || c.UserID <= 0 || cfg.Root == "" || cfg.StatePath == "" {
		return nil, errors.New("authenticated client, destination and binding state are required")
	}
	var err error
	cfg.Root, err = filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(cfg.Root) == cfg.Root {
		return nil, errors.New("choose a backup folder, not an entire drive root")
	}
	cfg.StatePath, err = filepath.Abs(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	if within(cfg.Root, cfg.StatePath) {
		return nil, errors.New("binding state must be outside the removable backup folder")
	}
	if cfg.PartBytes == 0 {
		cfg.PartBytes = 8 << 20
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = 30 * time.Second
	}
	if cfg.PartBytes < 1 || cfg.PartBytes > 8<<20 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 || cfg.ReserveBytes < 0 || cfg.PollInterval < time.Millisecond || cfg.RetryBase < time.Millisecond || cfg.InitDisk && cfg.AdoptDisk {
		return nil, errors.New("invalid download settings")
	}
	if err = os.MkdirAll(filepath.Dir(cfg.StatePath), 0700); err != nil {
		return nil, err
	}
	un, err := lockState(cfg.StatePath + ".lock")
	if err != nil {
		return nil, err
	}
	e := &PullEngine{cfg: cfg, client: c, unlock: un}
	fail := func(err error) (*PullEngine, error) { e.Close(); return nil, err }
	if info, err := os.Lstat(cfg.StatePath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 8192 {
			return fail(errors.New("invalid disk binding state"))
		}
		b, err := os.ReadFile(cfg.StatePath)
		if err != nil {
			return fail(err)
		}
		if err = json.Unmarshal(b, &e.binding); err != nil {
			return fail(errors.New("corrupt disk binding state"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	} else {
		if !cfg.InitDisk && !cfg.AdoptDisk {
			return fail(errors.New("initialize an empty backup folder with -init-disk, or explicitly recover an existing disk with -adopt-disk"))
		}
		info, err := os.Lstat(cfg.Root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("destination must be an existing directory, not a symlink"))
		}
		root, err := os.OpenRoot(cfg.Root)
		if err != nil {
			return fail(err)
		}
		defer root.Close()
		if cfg.InitDisk {
			f, err := root.Open(".")
			if err != nil {
				return fail(err)
			}
			names, err := f.Readdirnames(1)
			f.Close()
			if len(names) != 0 || err != io.EOF {
				return fail(errors.New("initialization requires an empty backup folder"))
			}
			label := strings.TrimSpace(cfg.DiskLabel)
			if !validRemoteName(label) || len(label) > 160 {
				return fail(errors.New("set a short disk label using -disk-label"))
			}
			id, err := newKey()
			if err != nil {
				return fail(err)
			}
			e.binding = DiskBinding{1, id, label, c.Endpoint, c.UserID, cfg.PartBytes}
			if err = writeDiskJSON(root, diskMarker, e.binding); err != nil {
				return fail(err)
			}
			initial := PullState{e.binding, map[string]PullEntry{}, map[string]string{}, map[string]RemoteFolder{}}
			if err = writeDiskJSON(root, diskManifest, initial); err != nil {
				return fail(err)
			}
		} else {
			b, err := readRegular(root, diskMarker, 8192)
			if err != nil {
				return fail(err)
			}
			if err = json.Unmarshal(b, &e.binding); err != nil {
				return fail(errors.New("invalid disk marker"))
			}
		}
		if (!cfg.RestoreToOtherServer && (e.binding.Endpoint != c.Endpoint || e.binding.UserID != c.UserID)) || e.binding.PartBytes != cfg.PartBytes {
			return fail(errors.New("disk belongs to another server, account or part size"))
		}
		if err = saveBinding(cfg.StatePath, e.binding); err != nil {
			return fail(err)
		}
	}
	if e.binding.Version != 1 || !idPattern.MatchString(e.binding.DiskID) || (!cfg.RestoreToOtherServer && (e.binding.Endpoint != c.Endpoint || e.binding.UserID != c.UserID)) || e.binding.PartBytes != cfg.PartBytes {
		return fail(errors.New("binding belongs to another server, account or part size"))
	}
	return e, nil
}
func (e *PullEngine) Close() {
	if e.unlock != nil {
		e.unlock()
		e.unlock = nil
	}
}

// Status reads the bound disk only; callers may display file IDs for explicit
// archive selection without exposing local credentials or capability tokens.
func (e *PullEngine) Status() ([]PullEntry, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, st, closeDisk, err := e.openDisk()
	if err != nil {
		return nil, err
	}
	defer closeDisk()
	ids := make([]string, 0, len(st.Latest))
	for id := range st.Latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]PullEntry, 0, len(ids))
	for _, id := range ids {
		out = append(out, st.Entries[st.Latest[id]])
	}
	return out, nil
}
func (e *PullEngine) checkDisk(root *os.Root) error {
	current, err := os.Lstat(e.cfg.Root)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup disk is unavailable; reconnect the original disk")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(current, opened) {
		return errors.New("backup disk changed during operation")
	}
	b, err := readRegular(root, diskMarker, 8192)
	if err != nil {
		return errors.New("backup disk marker unavailable")
	}
	var marker DiskBinding
	if json.Unmarshal(b, &marker) != nil || marker != e.binding {
		return errors.New("wrong backup disk or changed marker; refusing to write")
	}
	return nil
}
func (e *PullEngine) openDisk() (*os.Root, PullState, func(), error) {
	var st PullState
	root, err := os.OpenRoot(e.cfg.Root)
	if err != nil {
		return nil, st, nil, errors.New("backup disk unavailable; waiting for its original folder")
	}
	fail := func(err error) (*os.Root, PullState, func(), error) { root.Close(); return nil, st, nil, err }
	if err = e.checkDisk(root); err != nil {
		return fail(err)
	}
	un, err := lockState(filepath.Join(e.cfg.Root, ".netdisk-disk.lock"))
	if err != nil {
		return fail(err)
	}
	closeDisk := func() { un(); root.Close() }
	b, err := readRegular(root, diskManifest, 64<<20)
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	if err != nil || st.Binding != e.binding || st.Entries == nil || st.Latest == nil || st.Folders == nil {
		closeDisk()
		return nil, st, nil, errors.New("invalid disk manifest; retain it for recovery")
	}
	for key, v := range st.Entries {
		if !validRemoteFile(v.File) || key != fileRevision(v.File) || v.LocalPath != localVersion(v.File) || v.Attempts < 0 || v.ArchiveID != "" && !idPattern.MatchString(v.ArchiveID) || v.RestoreKey != "" && !idPattern.MatchString(v.RestoreKey) || v.RestoredFileID != "" && !idPattern.MatchString(v.RestoredFileID) || int64(len(v.Parts)) > (v.File.Size+e.cfg.PartBytes-1)/e.cfg.PartBytes {
			closeDisk()
			return nil, st, nil, errors.New("invalid backup version metadata")
		}
		for _, hash := range v.Parts {
			if !hashPattern.MatchString(hash) {
				closeDisk()
				return nil, st, nil, errors.New("invalid partial download state")
			}
		}
	}
	for id, key := range st.Latest {
		if v, ok := st.Entries[key]; !ok || v.File.ID != id {
			closeDisk()
			return nil, st, nil, errors.New("invalid latest version index")
		}
	}
	return root, st, closeDisk, nil
}
func (e *PullEngine) save(root *os.Root, st PullState) error {
	if err := e.checkDisk(root); err != nil {
		return &stateError{err}
	}
	if err := writeDiskJSON(root, diskManifest, st); err != nil {
		return &stateError{err}
	}
	return nil
}
func hashLocal(root *os.Root, name string) (string, int64, error) {
	f, err := openRegular(root, name, os.O_RDONLY)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 128<<10))
	if err != nil {
		return "", n, err
	}
	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", n, errors.New("backup changed while verifying")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func (e *PullEngine) download(ctx context.Context, root *os.Root, st *PullState, key string) error {
	v := st.Entries[key]
	if hash, n, err := hashLocal(root, v.LocalPath); err == nil {
		if hash != v.File.SHA256 || n != v.File.Size {
			return errors.New("existing local backup is corrupt; keep it for inspection")
		}
		v.Done = true
		v.Parts = nil
		st.Entries[key] = v
		return e.save(root, *st)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.MkdirAll("parts", 0700); err != nil {
		return err
	}
	partName := "parts/" + key + ".part"
	f, err := openRegular(root, partName, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		f, err = root.OpenFile(partName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	// Persisted chunks are rehashed before resuming; uncommitted/torn tails are discarded.
	offset := int64(0)
	good := 0
	for _, hash := range v.Parts {
		n := min(e.cfg.PartBytes, v.File.Size-offset)
		h := sha256.New()
		read, err := io.Copy(h, io.NewSectionReader(f, offset, n))
		if err != nil || read != n || hex.EncodeToString(h.Sum(nil)) != hash {
			break
		}
		good++
		offset += n
	}
	v.Parts = v.Parts[:good]
	v.Done = false
	if err = f.Truncate(offset); err != nil {
		return err
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	st.Entries[key] = v
	if err = e.save(root, *st); err != nil {
		return err
	}
	for offset < v.File.Size {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = e.checkDisk(root); err != nil {
			return err
		}
		n := min(e.cfg.PartBytes, v.File.Size-offset)
		free, err := diskFree(e.cfg.Root)
		if err != nil {
			return errors.New("cannot inspect backup disk free space")
		}
		if free < uint64(n)+uint64(e.cfg.ReserveBytes) {
			return errors.New("backup disk has insufficient free space")
		}
		resp, err := e.client.downloadRange(ctx, v.File, offset, offset+n-1)
		if err != nil {
			return err
		}
		h := sha256.New()
		written, readErr := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(resp.Body, n+1), make([]byte, 128<<10))
		resp.Body.Close()
		if readErr != nil || written != n {
			return errors.New("download interrupted; verified chunks retained")
		}
		if err = f.Sync(); err != nil {
			return err
		}
		v.Parts = append(v.Parts, hex.EncodeToString(h.Sum(nil)))
		offset += n
		st.Entries[key] = v
		if err = e.save(root, *st); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	hash, n, err := hashLocal(root, partName)
	if err != nil {
		return err
	}
	if hash != v.File.SHA256 || n != v.File.Size {
		v.Parts = nil
		st.Entries[key] = v
		if err = e.save(root, *st); err != nil {
			return err
		}
		return errors.New("download SHA-256 mismatch; partial content will be retried")
	}
	if err = e.checkDisk(root); err != nil {
		return err
	}
	if err = root.MkdirAll(filepath.ToSlash(filepath.Dir(v.LocalPath)), 0700); err != nil {
		return err
	}
	if _, err = root.Lstat(v.LocalPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup destination appeared during download")
	}
	if err = root.Rename(partName, v.LocalPath); err != nil {
		return err
	}
	v.Done = true
	v.Parts = nil
	st.Entries[key] = v
	return e.save(root, *st)
}
func (e *PullEngine) SyncOnce(ctx context.Context) (PullSummary, error) {
	if e.cfg.RestoreToOtherServer {
		return PullSummary{}, errors.New("recovery target can only restore existing backups")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var result PullSummary
	root, st, closeDisk, err := e.openDisk()
	if err != nil {
		return result, err
	}
	defer closeDisk()
	m, err := e.client.Manifest(ctx)
	if err != nil {
		return result, err
	}
	for _, folder := range m.Folders {
		st.Folders[folder.ID] = folder
	}
	for _, f := range m.Files {
		key := fileRevision(f)
		if _, ok := st.Entries[key]; !ok {
			st.Entries[key] = PullEntry{File: f, LocalPath: localVersion(f)}
		}
		st.Latest[f.ID] = key
	}
	if err = e.save(root, st); err != nil {
		return result, err
	}
	var problems []error
	for _, f := range m.Files {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		result.Found++
		key := fileRevision(f)
		v := st.Entries[key]
		if v.Done {
			hash, n, checkErr := hashLocal(root, v.LocalPath)
			if checkErr == nil && hash == f.SHA256 && n == f.Size {
				result.Skipped++
				continue
			}
			v.Done = false
		}
		if e.cfg.RetryFailed {
			v.Attempts = 0
			v.NextTry = 0
		}
		if v.Attempts >= e.cfg.MaxAttempts || time.Now().UnixMilli() < v.NextTry {
			result.Paused++
			continue
		}
		v.Attempts++
		st.Entries[key] = v
		if err = e.save(root, st); err != nil {
			return result, err
		}
		err = e.download(ctx, root, &st, key)
		if err == nil {
			result.Downloaded++
			e.log(f.Name, "downloaded and verified")
			continue
		}
		result.Failed++
		problems = append(problems, fmt.Errorf("%q: %w", f.Name, err))
		e.log(f.Name, "download incomplete; cloud file retained")
		var fatal *stateError
		if errors.As(err, &fatal) {
			return result, err
		}
		v = st.Entries[key]
		delay := e.cfg.RetryBase
		for i := 1; i < v.Attempts && delay < 15*time.Minute; i++ {
			delay *= 2
		}
		delay = min(delay, 15*time.Minute)
		v.NextTry = time.Now().Add(delay).UnixMilli()
		st.Entries[key] = v
		if saveErr := e.save(root, st); saveErr != nil {
			return result, saveErr
		}
	}
	e.cfg.RetryFailed = false
	return result, errors.Join(problems...)
}
func (e *PullEngine) log(path, status string) {
	if e.cfg.Log != nil {
		e.cfg.Log(Event{Path: path, Status: status})
	}
}
func (e *PullEngine) Run(ctx context.Context) error {
	for {
		_, err := e.SyncOnce(ctx)
		if err != nil {
			e.log("", "backup paused; reconnect the bound disk or resolve the reported failure")
		}
		timer := time.NewTimer(e.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (e *PullEngine) Archive(ctx context.Context, id string) (Archive, error) {
	if e.cfg.RestoreToOtherServer {
		return Archive{}, errors.New("archive is disabled for a different recovery target")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out Archive
	if !idPattern.MatchString(id) {
		return out, errors.New("select one file ID to archive")
	}
	root, st, closeDisk, err := e.openDisk()
	if err != nil {
		return out, err
	}
	defer closeDisk()
	key, ok := st.Latest[id]
	if !ok {
		return out, errors.New("file has no local backup")
	}
	v := st.Entries[key]
	if !v.Done {
		return out, errors.New("backup is incomplete; cloud file retained")
	}
	hash, n, err := hashLocal(root, v.LocalPath)
	if err != nil || hash != v.File.SHA256 || n != v.File.Size {
		return out, errors.New("local backup verification failed; cloud file retained")
	}
	if v.ArchiveID == "" {
		v.ArchiveID, err = newKey()
		if err != nil {
			return out, err
		}
		st.Entries[key] = v
		if err = e.save(root, st); err != nil {
			return out, err
		}
	}
	if err = e.checkDisk(root); err != nil {
		return out, err
	}
	out, err = e.client.archive(ctx, Archive{ID: v.ArchiveID, File: v.File, DiskID: e.binding.DiskID, DiskLabel: e.binding.Label, LocalPath: v.LocalPath})
	if err != nil {
		return out, err
	}
	v.ArchivedAt = out.ArchivedAt
	st.Entries[key] = v
	if err = e.save(root, st); err != nil {
		return out, err
	}
	return out, nil
}
func (e *PullEngine) Restore(ctx context.Context, id, revision string) (File, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out File
	root, st, closeDisk, err := e.openDisk()
	if err != nil {
		return out, err
	}
	defer closeDisk()
	key := st.Latest[id]
	if revision != "" {
		key = revision
	}
	v, ok := st.Entries[key]
	if !ok || v.File.ID != id || !v.Done {
		return out, errors.New("select a verified local backup")
	}
	target := fmt.Sprintf("%s#%d", e.client.Endpoint, e.client.UserID)
	if v.RestoreTarget != target {
		v.RestoredFileID = ""
		v.RestoreKey = ""
		v.RestoreTarget = target
	}
	hash, n, err := hashLocal(root, v.LocalPath)
	if err != nil || hash != v.File.SHA256 || n != v.File.Size {
		return out, errors.New("local backup verification failed")
	}
	m, err := e.client.Manifest(ctx)
	if err != nil {
		return out, err
	}
	for _, f := range m.Files {
		if (f.ID == v.RestoredFileID || !e.cfg.RestoreToOtherServer && f.ID == v.File.ID) && f.SHA256 == v.File.SHA256 && f.Size == v.File.Size && f.Name == v.File.Name {
			out = File{ID: f.ID, Name: f.Name, Size: f.Size}
			return out, e.ackRestore(ctx, v, out.ID)
		}
	}
	// A changed original is retained; restoring an older version publishes a new file.
	if v.RestoredFileID != "" {
		v.RestoredFileID = ""
		v.RestoreKey = ""
	}
	if v.File.Size > e.client.MaxUploadBytes {
		return File{}, errors.New("backup exceeds server upload limit")
	}
	if v.RestoreKey == "" {
		v.RestoreKey, err = newKey()
		if err != nil {
			return File{}, err
		}
	}
	st.Entries[key] = v
	if err = e.save(root, st); err != nil {
		return File{}, err
	}
	parent := ""
	for _, folder := range v.File.Folders {
		parent, err = e.client.ensureFolder(ctx, parent, folder.Name)
		if err != nil {
			return File{}, err
		}
	}
	source, err := openRegular(root, v.LocalPath, os.O_RDONLY)
	if err != nil {
		return File{}, err
	}
	defer source.Close()
	if err = e.checkDisk(root); err != nil {
		return File{}, err
	}
	out, err = e.client.restoreUpload(ctx, source, v, parent, e.cfg.PartBytes)
	if err != nil {
		return File{}, err
	}
	// Save the upload result before acknowledging the server archive. A retry
	// can finish the acknowledgement without publishing another file.
	v.RestoredFileID = out.ID
	st.Entries[key] = v
	if err = e.save(root, st); err != nil {
		return out, err
	}
	return out, e.ackRestore(ctx, v, out.ID)
}
func (e *PullEngine) ackRestore(ctx context.Context, v PullEntry, id string) error {
	if v.ArchivedAt <= 0 || e.cfg.RestoreToOtherServer {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"file_id": id})
	return e.client.request(ctx, "POST", "/api/archives/"+v.ArchiveID+"/restore", body, nil)
}
func (e *PullEngine) RestoreAll(ctx context.Context) error {
	e.mu.Lock()
	root, st, closeDisk, err := e.openDisk()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	_ = root
	closeDisk()
	e.mu.Unlock()
	// Recreate saved empty directories as well as paths needed by files.
	resolved := map[string]string{"": ""}
	visiting := map[string]bool{}
	var ensure func(string) (string, error)
	ensure = func(id string) (string, error) {
		if out, ok := resolved[id]; ok {
			return out, nil
		}
		f, ok := st.Folders[id]
		if !ok || visiting[id] || len(visiting) > 256 || !validRemoteName(f.Name) {
			return "", errors.New("invalid saved folder tree")
		}
		visiting[id] = true
		parent, err := ensure(f.ParentID)
		if err != nil {
			return "", err
		}
		out, err := e.client.ensureFolder(ctx, parent, f.Name)
		delete(visiting, id)
		if err == nil {
			resolved[id] = out
		}
		return out, err
	}
	for id := range st.Folders {
		if _, err = ensure(id); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(st.Latest))
	for id := range st.Latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var problems []error
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = e.Restore(ctx, id, ""); err != nil {
			problems = append(problems, err)
			e.log(id, "restore incomplete")
		} else {
			e.log(id, "restored")
		}
	}
	return errors.Join(problems...)
}
