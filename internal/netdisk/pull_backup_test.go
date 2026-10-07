package netdisk

import (
	"bingyan-netdisk/internal/backup"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pullFixture(t *testing.T, h *harness, handler http.Handler, name string) (*backup.Client, backup.PullConfig, *http.Cookie) {
	t.Helper()
	cookie := h.account(name)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := backup.NewClient(context.Background(), server.URL, name, "temporary-test-password")
	if err != nil {
		t.Fatal(err)
	}
	return c, backup.PullConfig{Root: t.TempDir(), StatePath: filepath.Join(t.TempDir(), "binding.json"), InitDisk: true, DiskLabel: "Synthetic removable disk", PartBytes: 4, MaxAttempts: 3, PollInterval: time.Millisecond, RetryBase: time.Millisecond}, cookie
}
func pullEngine(t *testing.T, cfg backup.PullConfig, c *backup.Client) *backup.PullEngine {
	t.Helper()
	e, err := backup.NewPull(cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func pullUpload(t *testing.T, h *harness, c *http.Cookie, folder, name, body string) File {
	t.Helper()
	w := h.request("POST", "/api/files?name="+url.QueryEscape(name)+"&folder_id="+folder, body, "application/octet-stream", c)
	status(t, w, 201)
	return parseFile(t, w)
}
func pullFolder(t *testing.T, h *harness, c *http.Cookie, parent, name string) Folder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"name": name, "parent_id": parent})
	w := h.request("POST", "/api/folders", string(b), "application/json", c)
	status(t, w, 201)
	var f Folder
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func pullState(t *testing.T, root string) backup.PullState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".netdisk-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st backup.PullState
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}
func pullEntry(t *testing.T, root, id string) backup.PullEntry {
	st := pullState(t, root)
	v, ok := st.Entries[st.Latest[id]]
	if !ok {
		t.Fatal("missing version")
	}
	return v
}
func pullOnce(t *testing.T, e *backup.PullEngine) backup.PullSummary {
	t.Helper()
	r, err := e.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPullBackupArchiveRestoreAndDeletionRetention(t *testing.T) {
	h := newHarness(t, 1<<20)
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			requests.Add(1)
		}
		h.handler.ServeHTTP(w, r)
	})
	c, cfg, cookie := pullFixture(t, h, handler, "disk_owner")
	other := h.account("disk_other")
	dir := pullFolder(t, h, cookie, "", "资料")
	empty := pullFolder(t, h, cookie, dir.ID, "empty")
	a := pullUpload(t, h, cookie, dir.ID, "CON:演示.txt", "hello disk backup")
	b := pullUpload(t, h, cookie, "", "zero.txt", "")
	shared := pullUpload(t, h, other, "", "same-content.txt", "hello disk backup")
	e := pullEngine(t, cfg, c)
	r := pullOnce(t, e)
	if r.Downloaded != 2 {
		t.Fatalf("initial download: %+v", r)
	}
	if len(pullState(t, cfg.Root).Folders) != 2 {
		t.Fatal("empty folder omitted")
	}
	before := requests.Load()
	r = pullOnce(t, e)
	if r.Skipped != 2 || requests.Load() != before {
		t.Fatal("unchanged backup downloaded again")
	}
	v := pullEntry(t, cfg.Root, a.ID)
	path := filepath.Join(cfg.Root, filepath.FromSlash(v.LocalPath))
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello disk backup" {
		t.Fatal("local bytes differ")
	}
	status(t, h.request("DELETE", "/api/files/"+b.ID, "", "", cookie), 204)
	pullOnce(t, e)
	if !pullEntry(t, cfg.Root, b.ID).Done {
		t.Fatal("remote deletion propagated")
	}
	ar, err := e.Archive(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	status(t, h.request("GET", "/api/files/"+a.ID+"/download", "", "", cookie), 404)
	status(t, h.request("GET", "/api/files/"+shared.ID+"/download", "", "", other), 200)
	if countQuery(t, h.app, "SELECT count(*) FROM disk_archives") != 1 || countQuery(t, h.app, "SELECT ref_count FROM blobs WHERE size=17") != 1 {
		t.Fatal("archive reference accounting failed")
	}
	if _, err = e.Archive(context.Background(), a.ID); err != nil || countQuery(t, h.app, "SELECT count(*) FROM disk_archives") != 1 {
		t.Fatal("archive replay not idempotent", err)
	}
	status(t, h.request("DELETE", "/api/folders/"+empty.ID, "", "", cookie), 204)
	status(t, h.request("DELETE", "/api/folders/"+dir.ID, "", "", cookie), 204)
	if err = e.RestoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, "SELECT count(*) FROM files") != 3 || countQuery(t, h.app, "SELECT count(*) FROM folders") != 2 {
		t.Fatal("restore did not restore files and empty directories")
	}
	restored := pullEntry(t, cfg.Root, a.ID).RestoredFileID
	if restored == "" || restored == a.ID {
		t.Fatal("restore needs a new file identity")
	}
	w := h.request("GET", "/api/files/"+restored+"/download", "", "", cookie)
	status(t, w, 200)
	if w.Body.String() != string(data) {
		t.Fatal("restored bytes differ")
	}
	if err = e.RestoreAll(context.Background()); err != nil || countQuery(t, h.app, "SELECT count(*) FROM files") != 3 {
		t.Fatal("repeated restore duplicated files", err)
	}
	var marked string
	if err = h.app.db.QueryRow(`SELECT restored_file_id FROM disk_archives WHERE id=?`, ar.ID).Scan(&marked); err != nil || marked != restored {
		t.Fatal("restore record not acknowledged")
	}
	e.Close()
	cfg.InitDisk = false
	e = pullEngine(t, cfg, c)
	e.Close()
	contents, _ := os.ReadFile(filepath.Join(cfg.Root, ".netdisk-manifest.json"))
	if strings.Contains(string(contents), "temporary-test-password") || strings.Contains(string(contents), cookieName) {
		t.Fatal("credentials in disk manifest")
	}
}

func TestPullResumeAcrossRestartAndCorruptPartial(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "corrupt-prefix"}[corrupt], func(t *testing.T) {
			h := newHarness(t, 1024)
			var fail atomic.Bool
			fail.Store(true)
			var first atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/download") {
					if r.Header.Get("Range") == "bytes=0-3" {
						first.Add(1)
					}
					if fail.Load() && r.Header.Get("Range") == "bytes=4-7" {
						w.WriteHeader(503)
						return
					}
				}
				h.handler.ServeHTTP(w, r)
			})
			c, cfg, cookie := pullFixture(t, h, handler, "resume_disk")
			f := pullUpload(t, h, cookie, "", "payload.bin", "abcdefghijkl")
			e := pullEngine(t, cfg, c)
			r, err := e.SyncOnce(context.Background())
			if err == nil || r.Failed != 1 {
				t.Fatal("failure not retained")
			}
			e.Close()
			st := pullState(t, cfg.Root)
			key := st.Latest[f.ID]
			if len(st.Entries[key].Parts) != 1 {
				t.Fatal("first chunk not saved")
			}
			if corrupt {
				if err = os.WriteFile(filepath.Join(cfg.Root, "parts", key+".part"), []byte("BAD!"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fail.Store(false)
			cfg.InitDisk = false
			cfg.RetryFailed = true
			e = pullEngine(t, cfg, c)
			defer e.Close()
			r = pullOnce(t, e)
			want := int32(1)
			if corrupt {
				want = 2
			}
			if r.Downloaded != 1 || first.Load() != want {
				t.Fatalf("resume repeated verified bytes: %+v first=%d", r, first.Load())
			}
			v := pullEntry(t, cfg.Root, f.ID)
			b, _ := os.ReadFile(filepath.Join(cfg.Root, filepath.FromSlash(v.LocalPath)))
			if string(b) != "abcdefghijkl" {
				t.Fatal("resumed bytes differ")
			}
		})
	}
}

func TestPullDiskIdentityCorruptionAndArchiveCompareGuards(t *testing.T) {
	h := newHarness(t, 1024)
	c, cfg, cookie := pullFixture(t, h, h.handler, "guard_disk")
	f := pullUpload(t, h, cookie, "", "original.txt", "safe data")
	e := pullEngine(t, cfg, c)
	defer e.Close()
	pullOnce(t, e)
	if _, err := backup.NewPull(cfg, c); err == nil {
		t.Fatal("second process acquired binding")
	}
	original := cfg.Root + "-unplugged"
	if err := os.Rename(cfg.Root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SyncOnce(context.Background()); err == nil {
		t.Fatal("wrote to replacement disk")
	}
	if _, err := e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("archive accepted missing disk")
	}
	entries, _ := os.ReadDir(cfg.Root)
	if len(entries) != 0 {
		t.Fatal("replacement folder was modified")
	}
	if err := os.Remove(cfg.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, cfg.Root); err != nil {
		t.Fatal(err)
	}
	v := pullEntry(t, cfg.Root, f.ID)
	local := filepath.Join(cfg.Root, filepath.FromSlash(v.LocalPath))
	if err := os.WriteFile(local, []byte("bad bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("corrupt backup archived")
	}
	if err := os.WriteFile(local, []byte("safe data"), 0600); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("PATCH", "/api/files/"+f.ID, `{"name":"changed.txt"}`, "application/json", cookie), 200)
	if _, err := e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("stale metadata archived")
	}
	if backupRows(t, h) != 1 {
		t.Fatal("guard failure removed cloud data")
	}
	pullOnce(t, e)
	if len(pullState(t, cfg.Root).Entries) != 2 {
		t.Fatal("rename history lost")
	}
	if _, err := e.Archive(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPullInvalidRangeHashSpaceAndPathIsolation(t *testing.T) {
	for _, fault := range []string{"range", "hash", "space", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			h := newHarness(t, 1024)
			var downloads atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/download") {
					downloads.Add(1)
					if fault == "range" {
						w.Header().Set("Content-Range", "bytes 1-4/4")
						w.WriteHeader(206)
						w.Write([]byte("data"))
						return
					}
					if fault == "hash" {
						w.Header().Set("Content-Range", "bytes 0-3/4")
						w.WriteHeader(206)
						w.Write([]byte("evil"))
						return
					}
				}
				h.handler.ServeHTTP(w, r)
			})
			c, cfg, cookie := pullFixture(t, h, handler, "bad_download")
			f := pullUpload(t, h, cookie, "", "payload.txt", "data")
			if fault == "space" {
				cfg.ReserveBytes = 1 << 60
			}
			e := pullEngine(t, cfg, c)
			defer e.Close()
			outside := t.TempDir()
			if fault == "symlink" {
				if err := os.Symlink(outside, filepath.Join(cfg.Root, "parts")); err != nil {
					t.Skip("symlink creation unavailable")
				}
			}
			r, err := e.SyncOnce(context.Background())
			if err == nil || r.Failed != 1 {
				t.Fatalf("unsafe download accepted: %+v", r)
			}
			if _, err = e.Archive(context.Background(), f.ID); err == nil {
				t.Fatal("unverified content archived")
			}
			if backupRows(t, h) != 1 {
				t.Fatal("failure deleted remote")
			}
			if fault == "space" && downloads.Load() != 0 {
				t.Fatal("download started despite full disk")
			}
			items, _ := os.ReadDir(outside)
			if len(items) != 0 {
				t.Fatal("wrote outside bound disk")
			}
		})
	}
}

func TestArchiveOwnershipReceiptLossAndServerRestart(t *testing.T) {
	h := newHarness(t, 1024)
	var drop atomic.Bool
	drop.Store(true)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/archives" && drop.Swap(false) {
			record := httptest.NewRecorder()
			h.handler.ServeHTTP(record, r)
			if record.Code != 201 {
				t.Errorf("archive failed before simulated response loss")
			}
			w.WriteHeader(503)
			return
		}
		h.handler.ServeHTTP(w, r)
	})
	c, cfg, cookie := pullFixture(t, h, handler, "receipt_owner")
	other := h.account("receipt_other")
	f := pullUpload(t, h, cookie, "", "receipt.txt", "bytes")
	e := pullEngine(t, cfg, c)
	pullOnce(t, e)
	status(t, h.request("GET", "/api/backup/manifest", "", "", nil), 401)
	w := h.request("GET", "/api/backup/manifest", "", "", other)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), f.ID) {
		t.Fatal("other account can list manifest")
	}
	v := pullEntry(t, cfg.Root, f.ID)
	body := backup.Archive{ID: strings.Repeat("a", 32), File: v.File, DiskID: strings.Repeat("b", 32), DiskLabel: "test disk", LocalPath: v.LocalPath}
	encoded, _ := json.Marshal(body)
	status(t, h.request("POST", "/api/archives", string(encoded), "application/json", other), 404)
	body.LocalPath = "../escape"
	encoded, _ = json.Marshal(body)
	status(t, h.request("POST", "/api/archives", string(encoded), "application/json", cookie), 400)
	if _, err := e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("response loss not simulated")
	}
	e.Close()
	if backupRows(t, h) != 0 || countQuery(t, h.app, "SELECT count(*) FROM disk_archives") != 1 {
		t.Fatal("archive transaction not committed exactly once")
	}
	config := h.app.cfg
	if err := h.app.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.app, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = h.app.Handler()
	cfg.InitDisk = false
	e = pullEngine(t, cfg, c)
	defer e.Close()
	if _, err = e.Archive(context.Background(), f.ID); err != nil {
		t.Fatal("receipt recovery failed", err)
	}
	w = h.request("GET", "/api/archives", "", "", other)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), f.ID) {
		t.Fatal("other account can see archives")
	}
	if _, err = e.Restore(context.Background(), f.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = h.app.db.QueryRow("PRAGMA integrity_check").Scan(new(string)); err != nil {
		t.Fatal(err)
	}
}

func TestPullAdoptDiskAndPersistentRetryBudget(t *testing.T) {
	h := newHarness(t, 1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			w.WriteHeader(503)
			return
		}
		h.handler.ServeHTTP(w, r)
	})
	c, cfg, cookie := pullFixture(t, h, handler, "bounded_disk")
	f := pullUpload(t, h, cookie, "", "file.txt", "data")
	cfg.MaxAttempts = 1
	e := pullEngine(t, cfg, c)
	if _, err := e.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	e.Close()
	cfg.InitDisk = false
	e = pullEngine(t, cfg, c)
	r := pullOnce(t, e)
	if r.Paused != 1 || pullEntry(t, cfg.Root, f.ID).Attempts != 1 {
		t.Fatal("retry budget reset")
	}
	e.Close()
	cfg.StatePath = filepath.Join(t.TempDir(), "recovered.json")
	if _, err := backup.NewPull(cfg, c); err == nil {
		t.Fatal("silently rebound existing disk")
	}
	cfg.AdoptDisk = true
	e = pullEngine(t, cfg, c)
	defer e.Close()
	r = pullOnce(t, e)
	if r.Paused != 1 {
		t.Fatal("adoption lost download state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("watcher did not cancel")
	}
}

func TestPullChangedContentHistoricalRestoreAndDisasterRecoveryTarget(t *testing.T) {
	h := newHarness(t, 1024)
	c, cfg, cookie := pullFixture(t, h, h.handler, "history_disk")
	folder := pullFolder(t, h, cookie, "", "saved-folder")
	pullFolder(t, h, cookie, folder.ID, "empty-child")
	f := pullUpload(t, h, cookie, folder.ID, "version.txt", "before")
	e := pullEngine(t, cfg, c)
	pullOnce(t, e)
	oldRevision := pullState(t, cfg.Root).Latest[f.ID]
	tmp := filepath.Join(h.app.temp, "synthetic-nfs-replacement")
	if err := os.WriteFile(tmp, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("after"))
	h.app.mu.Lock()
	_, err := h.app.publishReplacing(context.Background(), c.UserID, f.Name, folder.ID, 5, fmt.Sprintf("%x", hash), tmp, f.ID)
	if err == nil {
		err = h.app.gcBlobs(context.Background())
	} // Same commit sequence as logicalFile.Close.
	h.app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("content changed but archive was accepted")
	}
	pullOnce(t, e)
	if len(pullState(t, cfg.Root).Entries) != 2 {
		t.Fatal("older content version was discarded")
	}
	restored, err := e.Restore(context.Background(), f.ID, oldRevision)
	if err != nil || restored.ID == f.ID {
		t.Fatal("historical restore replaced current content", err)
	}
	current := h.request("GET", "/api/files/"+f.ID+"/download", "", "", cookie)
	status(t, current, 200)
	if current.Body.String() != "after" {
		t.Fatal("current remote version was overwritten")
	}
	e.Close()
	cfg.InitDisk = false
	other := newHarness(t, 1024)
	dest, _, destCookie := pullFixture(t, other, other.handler, "recovery_owner")
	if _, err = backup.NewPull(cfg, dest); err == nil {
		t.Fatal("implicit rebind to other server accepted")
	}
	cfg.RestoreToOtherServer = true
	e = pullEngine(t, cfg, dest)
	defer e.Close()
	if _, err = e.SyncOnce(context.Background()); err == nil {
		t.Fatal("recovery target could mutate backup inventory")
	}
	if _, err = e.Archive(context.Background(), f.ID); err == nil {
		t.Fatal("recovery target could archive")
	}
	if err = e.RestoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backupRows(t, other) != 1 || countQuery(t, other.app, "SELECT count(*) FROM folders") != 2 {
		t.Fatal("recovery target missed files or empty directories")
	}
	if err = e.RestoreAll(context.Background()); err != nil || backupRows(t, other) != 1 {
		t.Fatal("recovery target duplicated files", err)
	}
	restored, err = e.Restore(context.Background(), f.ID, oldRevision)
	if err != nil {
		t.Fatal(err)
	}
	w := other.request("GET", "/api/files/"+restored.ID+"/download", "", "", destCookie)
	status(t, w, 200)
	if w.Body.String() != "before" {
		t.Fatal("historical recovery bytes differ")
	}
}

func TestArchiveRestoreAcknowledgementRetry(t *testing.T) {
	h := newHarness(t, 1024)
	var drop atomic.Bool
	drop.Store(true)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restore") && drop.Swap(false) {
			w.WriteHeader(503)
			return
		}
		h.handler.ServeHTTP(w, r)
	})
	c, cfg, cookie := pullFixture(t, h, handler, "ack_retry")
	f := pullUpload(t, h, cookie, "", "file.txt", "data")
	e := pullEngine(t, cfg, c)
	defer e.Close()
	pullOnce(t, e)
	ar, err := e.Archive(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Restore(context.Background(), f.ID, ""); err == nil {
		t.Fatal("ack failure not simulated")
	}
	if backupRows(t, h) != 1 {
		t.Fatal("restored upload not committed")
	}
	out, err := e.Restore(context.Background(), f.ID, "")
	if err != nil || backupRows(t, h) != 1 {
		t.Fatal("retry duplicated restore", err)
	}
	var id string
	if err = h.app.db.QueryRow("SELECT restored_file_id FROM disk_archives WHERE id=?", ar.ID).Scan(&id); err != nil || id != out.ID {
		t.Fatal("archive acknowledgement not recovered")
	}
}
