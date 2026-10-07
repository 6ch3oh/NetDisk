package netdisk

import (
	"bingyan-netdisk/internal/backup"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func backupFixture(t *testing.T, h *harness, handler http.Handler, name string) (*backup.Client, backup.Config) {
	t.Helper()
	h.account(name)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := backup.NewClient(context.Background(), server.URL, name, "temporary-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := backup.Config{Root: t.TempDir(), StatePath: filepath.Join(t.TempDir(), "state.json"), RemoteName: "fixture-backup", Concurrency: 2, MaxAttempts: 3, PollInterval: 10 * time.Millisecond, RetryBase: time.Millisecond, PartBytes: 4}
	return client, cfg
}
func backupEngine(t *testing.T, cfg backup.Config, c *backup.Client) *backup.Engine {
	t.Helper()
	e, err := backup.New(cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func backupWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func backupCycle(t *testing.T, e *backup.Engine) backup.Summary {
	t.Helper()
	result, err := e.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func backupWait(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("backup watcher condition timed out")
}
func backupRows(t *testing.T, h *harness) int {
	t.Helper()
	return countQuery(t, h.app, "SELECT count(*) FROM files WHERE deleted=0")
}

func TestBackupWatcherNewModifiedRestartDeletionDedupeMappingAndBoundary(t *testing.T) {
	h := newHarness(t, 1024)
	client, cfg := backupFixture(t, h, h.handler, "backup_a")
	backupWrite(t, cfg.Root, "a.txt", "shared bytes")
	backupWrite(t, cfg.Root, "nested/汉字 & b.txt", "shared bytes")
	backupWrite(t, cfg.Root, "empty.txt", "")
	outside := t.TempDir()
	backupWrite(t, outside, "must-not-upload.txt", "outside explicit root")
	if err := os.Symlink(outside, filepath.Join(cfg.Root, "escape")); err != nil {
		t.Log("symlink creation unavailable on this platform")
	}
	e := backupEngine(t, cfg, client)
	r := backupCycle(t, e)
	if r.Uploaded != 3 || backupRows(t, h) != 3 || countQuery(t, h.app, "SELECT count(*) FROM blobs") != 2 {
		t.Fatalf("initial backup: %+v", r)
	}
	if countQuery(t, h.app, "SELECT ref_count FROM blobs WHERE size=12") != 2 {
		t.Fatal("dedupe refs are not shared")
	}
	if _, err := backup.New(cfg, client); err == nil {
		t.Fatal("second process acquired the state lock")
	}
	e.Close()
	e = backupEngine(t, cfg, client)
	if r = backupCycle(t, e); r.Uploaded != 0 || r.Skipped != 3 {
		t.Fatalf("restart duplicated versions: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	backupWrite(t, cfg.Root, "new/deep/new.txt", "new file content")
	backupWait(t, func() bool { return backupRows(t, h) == 4 })
	backupWrite(t, cfg.Root, "a.txt", "modified bytes")
	backupWait(t, func() bool { return backupRows(t, h) == 5 })
	if err := os.Remove(filepath.Join(cfg.Root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r = backupCycle(t, e); r.Found != 3 || r.Uploaded != 0 || backupRows(t, h) != 5 {
		t.Fatalf("deletion propagated: %+v", r)
	}
	data, err := os.ReadFile(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "netdisk_session") {
		t.Fatal("credentials in state")
	}
	if countQuery(t, h.app, "SELECT count(*) FROM folders") != 4 {
		t.Fatal("relative directory mapping incorrect")
	}
	e.Close()
	cfg.StatePath = filepath.Join(t.TempDir(), "recovered.json")
	e = backupEngine(t, cfg, client)
	defer e.Close()
	r = backupCycle(t, e)
	if r.Uploaded != 3 || backupRows(t, h) != 5 {
		t.Fatalf("state recovery duplicated versions: %+v", r)
	}
	t.Log("real HTTP watcher: initial 3 files; new+modified; unchanged restart; local delete retained 5 cloud files; dedupe+path+symlink boundary PASS")
}

func TestBackupPartFailureIsolationResumeAndBackendRestart(t *testing.T) {
	h := newHarness(t, 1024)
	var fail atomic.Bool
	fail.Store(true)
	var firstParts, active, peak atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/parts/") {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			if strings.HasSuffix(r.URL.Path, "/0") {
				firstParts.Add(1)
			}
			if strings.HasSuffix(r.URL.Path, "/1") && fail.Load() {
				http.Error(w, "injected interruption", 503)
				return
			}
		}
		h.handler.ServeHTTP(w, r)
	})
	client, cfg := backupFixture(t, h, handler, "backup_resume")
	backupWrite(t, cfg.Root, "resume.bin", "abcdefghijkl")
	backupWrite(t, cfg.Root, "healthy.txt", "good")
	e := backupEngine(t, cfg, client)
	result, err := e.SyncOnce(context.Background())
	if err == nil || result.Uploaded != 1 || result.Failed != 1 || backupRows(t, h) != 1 {
		t.Fatalf("failure isolation: %+v, %v", result, err)
	}
	if countQuery(t, h.app, "SELECT count(*) FROM upload_parts") != 1 {
		t.Fatal("first part not persisted")
	}
	e.Close()
	restartHarness(t, h)
	fail.Store(false)
	e = backupEngine(t, cfg, client)
	defer e.Close()
	time.Sleep(5 * time.Millisecond)
	result = backupCycle(t, e)
	if result.Uploaded != 1 || result.Skipped != 1 || backupRows(t, h) != 2 || firstParts.Load() != 2 || peak.Load() > 2 {
		t.Fatalf("resume: %+v firstParts=%d peak=%d", result, firstParts.Load(), peak.Load())
	}
	if countQuery(t, h.app, "SELECT count(*) FROM upload_sessions") != 0 {
		t.Fatal("completed session retained")
	}
	t.Log("real HTTP controlled 503: healthy file completed; saved part skipped after client+backend restart; bounded concurrency PASS")
}

func TestBackupLostCompletionExpiredReceiptAndIdentity(t *testing.T) {
	h := newHarness(t, 1024)
	var lose atomic.Bool
	lose.Store(true)
	var creates atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/uploads" {
			creates.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/complete") && lose.Swap(false) {
			recorded := httptest.NewRecorder()
			h.handler.ServeHTTP(recorded, r)
			if recorded.Code != 201 {
				t.Errorf("real completion failed: %d", recorded.Code)
			}
			http.Error(w, "injected lost response", 503)
			return
		}
		h.handler.ServeHTTP(w, r)
	})
	client, cfg := backupFixture(t, h, handler, "backup_receipt")
	backupWrite(t, cfg.Root, "receipt.txt", "abcdefgh")
	e := backupEngine(t, cfg, client)
	if _, err := e.SyncOnce(context.Background()); err == nil || backupRows(t, h) != 1 {
		t.Fatal("completion fault did not follow publication")
	}
	e.Close()
	if _, err := h.app.db.Exec("UPDATE upload_requests SET expires_at=0"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	e = backupEngine(t, cfg, client)
	time.Sleep(5 * time.Millisecond)
	if r := backupCycle(t, e); r.Uploaded != 1 || backupRows(t, h) != 1 || creates.Load() != 1 {
		t.Fatalf("lost receipt duplicated upload: %+v creates=%d", r, creates.Load())
	}
	e.Close()
	h.account("backup_other")
	other, err := backup.NewClient(context.Background(), client.Endpoint, "backup_other", "temporary-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.New(cfg, other); err == nil {
		t.Fatal("cross-user state accepted")
	}
	changed := cfg
	changed.RemoteName = "another-target"
	if _, err = backup.New(changed, client); err == nil {
		t.Fatal("cross-target state accepted")
	}
	t.Log("committed response loss + expired receipt reconciled by remote hash without another upload; state identity isolation PASS")
}

func TestBackupPersistentRetryCeilingAndExplicitRecovery(t *testing.T) {
	h := newHarness(t, 1024)
	var fail atomic.Bool
	fail.Store(true)
	var failures atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/parts/") && fail.Load() {
			failures.Add(1)
			http.Error(w, "injected outage", 503)
			return
		}
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/files") {
			t.Error("backup deleted a cloud file")
		}
		h.handler.ServeHTTP(w, r)
	})
	client, cfg := backupFixture(t, h, handler, "backup_budget")
	cfg.MaxAttempts = 2
	backupWrite(t, cfg.Root, "retry.txt", "old")
	e := backupEngine(t, cfg, client)
	for i := 0; i < 2; i++ {
		if _, err := e.SyncOnce(context.Background()); err == nil {
			t.Fatal("expected outage")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.Close()
	e = backupEngine(t, cfg, client)
	for i := 0; i < 3; i++ {
		r := backupCycle(t, e)
		if r.Paused != 1 || failures.Load() != 2 {
			t.Fatalf("unbounded retry: %+v requests=%d", r, failures.Load())
		}
	}
	e.Close()
	fail.Store(false)
	cfg.RetryFailed = true
	e = backupEngine(t, cfg, client)
	if r := backupCycle(t, e); r.Uploaded != 1 {
		t.Fatalf("explicit retry: %+v", r)
	}
	e.Close()
	cfg.RetryFailed = false
	backupWrite(t, cfg.Root, "retry.txt", "new")
	e = backupEngine(t, cfg, client)
	if r := backupCycle(t, e); r.Uploaded != 1 || backupRows(t, h) != 2 {
		t.Fatalf("new content: %+v", r)
	}
	e.Close()
	if err := os.WriteFile(cfg.StatePath, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.New(cfg, client); err == nil {
		t.Fatal("corrupt state accepted")
	}
	t.Log("persistent retry ceiling, explicit retry and changed-content budget reset; versions retained; corrupt state refused PASS")
}

func TestBackupPendingSourceChangeCancelsOnlyOwnSession(t *testing.T) {
	h := newHarness(t, 1024)
	var fail atomic.Bool
	fail.Store(true)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/parts/") && fail.Load() {
			http.Error(w, "injected outage", 503)
			return
		}
		h.handler.ServeHTTP(w, r)
	})
	client, cfg := backupFixture(t, h, handler, "backup_change")
	backupWrite(t, cfg.Root, "changing.txt", "before")
	e := backupEngine(t, cfg, client)
	defer e.Close()
	if _, err := e.SyncOnce(context.Background()); err == nil {
		t.Fatal("missing outage")
	}
	backupWrite(t, cfg.Root, "changing.txt", "after")
	fail.Store(false)
	if r := backupCycle(t, e); r.Uploaded != 1 {
		t.Fatalf("pending source change: %+v", r)
	}
	if countQuery(t, h.app, "SELECT count(*) FROM upload_sessions") != 0 || countQuery(t, h.app, "SELECT count(*) FROM upload_requests") != 1 || backupRows(t, h) != 1 {
		t.Fatal("superseded reservation not cancelled")
	}
	var persisted struct {
		Entries map[string]backup.Entry `json:"entries"`
	}
	data, _ := os.ReadFile(cfg.StatePath)
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	fileID := persisted.Entries["changing.txt"].FileID
	if !keyPattern.MatchString(fileID) {
		t.Fatal("missing file ID")
	}
	cookie := h.account("backup_idor")
	status(t, h.request("GET", fmt.Sprintf("/api/files/%s/download", fileID), "", "", cookie), 404)
	t.Log("changed source cancelled only its incomplete session, released quota, published new version; cross-user download rejected PASS")
}

func TestBackupSourceChangesDuringPartUploadNeverPublishesMixedVersion(t *testing.T) {
	h := newHarness(t, 1024)
	var change atomic.Bool
	change.Store(true)
	var root string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/parts/0") && change.Swap(false) {
			if err := os.WriteFile(filepath.Join(root, "race.bin"), []byte("123456789abc"), 0600); err != nil {
				t.Error(err)
			}
		}
		h.handler.ServeHTTP(w, r)
	})
	client, cfg := backupFixture(t, h, handler, "backup_race")
	root = cfg.Root
	backupWrite(t, root, "race.bin", "abcdefghijkl")
	e := backupEngine(t, cfg, client)
	defer e.Close()
	if _, err := e.SyncOnce(context.Background()); err == nil || backupRows(t, h) != 0 {
		t.Fatal("mixed source was published")
	}
	if r := backupCycle(t, e); r.Uploaded != 1 || backupRows(t, h) != 1 {
		t.Fatalf("new stable version was not recovered: %+v", r)
	}
	var state struct {
		Entries map[string]backup.Entry `json:"entries"`
	}
	data, _ := os.ReadFile(cfg.StatePath)
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	// Authenticate in-memory only, then check actual served bytes.
	w := h.request("POST", "/api/login", "{\"username\":\"backup_race\",\"password\":\"temporary-test-password\"}", "application/json", nil)
	status(t, w, 200)
	c := w.Result().Cookies()[0]
	got := h.request("GET", "/api/files/"+state.Entries["race.bin"].FileID+"/download", "", "", c)
	status(t, got, 200)
	if got.Body.String() != "123456789abc" {
		t.Fatal("mixed bytes persisted")
	}
	t.Log("in-place change during real part request: mixed digest rejected before publish; subsequent stable version recovered PASS")
}

func TestBackupSessionRefreshAndTamperedCancelStateRejected(t *testing.T) {
	h := newHarness(t, 1024)
	client, cfg := backupFixture(t, h, h.handler, "backup_refresh")
	backupWrite(t, cfg.Root, "one.txt", "first")
	e := backupEngine(t, cfg, client)
	backupCycle(t, e)
	if _, err := h.app.db.Exec("UPDATE sessions SET expires_at=0"); err != nil {
		t.Fatal(err)
	}
	backupWrite(t, cfg.Root, "two.txt", "second")
	if r := backupCycle(t, e); r.Uploaded != 1 || r.Skipped != 1 {
		t.Fatalf("expired login was not refreshed: %+v", r)
	}
	e.Close()
	data, err := os.ReadFile(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]json.RawMessage
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	var entries map[string]backup.Entry
	if err = json.Unmarshal(st["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	v := entries["one.txt"]
	v.CancelID = "../files/not-an-upload"
	entries["one.txt"] = v
	st["entries"], _ = json.Marshal(entries)
	data, _ = json.Marshal(st)
	if err = os.WriteFile(cfg.StatePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = backup.New(cfg, client); err == nil {
		t.Fatal("tampered cancellation ID accepted")
	}
	if backupRows(t, h) != 2 {
		t.Fatal("tampered state affected cloud files")
	}
	t.Log("expired Session re-login and retry succeeded; malicious cancellation path rejected before remote mutation PASS")
}
