package netdisk

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func randomContent(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func putFile(t *testing.T, h *harness, c *http.Cookie, name, folder string, b []byte) File {
	t.Helper()
	w := h.reader("POST", "/api/files?name="+name+"&folder_id="+folder, bytes.NewReader(b), "application/octet-stream", c)
	status(t, w, 201)
	return parseFile(t, w)
}
func countQuery(t *testing.T, a *App, q string, args ...any) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func entries(t *testing.T, dir string) int {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(e)
}
func sameContent(t *testing.T, got, want []byte) {
	t.Helper()
	if sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatal("content hash mismatch")
	}
}

func TestConcurrentDedupAndLastReference(t *testing.T) {
	h := newHarness(t, 1<<20)
	a := h.account("dedup_a")
	b := h.account("dedup_b")
	content := randomContent(t, 256<<10)
	var fs [2]File
	var wg sync.WaitGroup
	for i, c := range []*http.Cookie{a, b} {
		wg.Add(1)
		go func(i int, c *http.Cookie) { defer wg.Done(); fs[i] = putFile(t, h, c, "same.bin", "", content) }(i, c)
	}
	wg.Wait()
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 2 || countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 1 || countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 2 || entries(t, h.app.blobs) != 1 {
		t.Fatal("duplicate physical blob or bad references")
	}
	status(t, h.request("DELETE", "/api/files/"+fs[0].ID, "", "", a), 204)
	w := h.request("GET", fs[1].DownloadURL, "", "", b)
	status(t, w, 200)
	sameContent(t, w.Body.Bytes(), content)
	if entries(t, h.app.blobs) != 1 || countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 1 {
		t.Fatal("shared blob deleted early")
	}
	status(t, h.request("DELETE", "/api/files/"+fs[1].ID, "", "", b), 204)
	if entries(t, h.app.blobs) != 0 || countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 0 {
		t.Fatal("last reference not reclaimed")
	}
}
func createShareTest(t *testing.T, h *harness, c *http.Cookie, kind, id string) (Share, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"resource_type": kind, "resource_id": id})
	w := h.request("POST", "/api/shares", string(body), "application/json", c)
	status(t, w, 201)
	var out struct {
		Share Share  `json:"share"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Share, out.URL
}
func TestShareCapabilitiesSubtreeRevocationAndDeletion(t *testing.T) {
	h := newHarness(t, 4096)
	a := h.account("share_a")
	b := h.account("share_b")
	root := makeFolder(t, h, a, "root", "")
	child := makeFolder(t, h, a, "child", root.ID)
	sibling := makeFolder(t, h, a, "sibling", "")
	payload := randomContent(t, 512)
	f := putFile(t, h, a, "file.bin", child.ID, payload)
	outside := putFile(t, h, a, "outside.bin", sibling.ID, payload)
	s, url := createShareTest(t, h, a, "folder", root.ID)
	status(t, h.request("GET", url, "", "", nil), 200)
	w := h.request("GET", url+"?folder_id="+child.ID, "", "", nil)
	status(t, w, 200)
	if bytes.Contains(w.Body.Bytes(), []byte("/api/files")) || bytes.Contains(w.Body.Bytes(), []byte("breadcrumbs")) {
		t.Fatal("shared listing leaked private navigation")
	}
	for _, id := range []string{sibling.ID, "../", "root"} {
		status(t, h.request("GET", url+"?folder_id="+id, "", "", nil), 404)
	}
	w = h.request("GET", url+"/files/"+f.ID+"/download", "", "", nil)
	status(t, w, 200)
	sameContent(t, w.Body.Bytes(), payload)
	status(t, h.request("GET", url+"/files/"+outside.ID+"/download", "", "", nil), 404)
	status(t, h.request("DELETE", "/api/shares/"+s.ID, "", "", b), 404)
	status(t, h.request("POST", "/api/shares", `{"resource_type":"file","resource_id":"`+f.ID+`"}`, "application/json", b), 404)
	w = h.request("GET", "/api/shares", "", "", a)
	status(t, w, 200)
	if !bytes.Contains(w.Body.Bytes(), []byte(s.ID)) {
		t.Fatal("owner share missing from list")
	}
	foreignList := h.request("GET", "/api/shares", "", "", b)
	status(t, foreignList, 200)
	if bytes.Contains(foreignList.Body.Bytes(), []byte(s.ID)) {
		t.Fatal("share list leaked cross-user metadata")
	}

	// Moving a resource outside a shared folder removes the capability immediately.
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", sibling.ID), "application/json", a), 204)
	status(t, h.request("GET", url+"/files/"+f.ID+"/download", "", "", nil), 404)
	status(t, h.request("DELETE", "/api/shares/"+s.ID, "", "", a), 204)
	status(t, h.request("GET", url, "", "", nil), 404)
	_, fileURL := createShareTest(t, h, a, "file", f.ID)
	status(t, h.request("GET", fileURL, "", "", nil), 200)
	status(t, h.request("GET", fileURL+"/files/"+outside.ID+"/download", "", "", nil), 404)
	status(t, h.request("DELETE", "/api/files/"+f.ID, "", "", a), 204)
	status(t, h.request("GET", fileURL, "", "", nil), 404)
	empty := makeFolder(t, h, a, "empty", "")
	_, emptyURL := createShareTest(t, h, a, "folder", empty.ID)
	status(t, h.request("DELETE", "/api/folders/"+empty.ID, "", "", a), 204)
	status(t, h.request("GET", emptyURL, "", "", nil), 404)
	if countQuery(t, h.app, `SELECT count(*) FROM shares`) != 0 {
		t.Fatal("invalid shares retained")
	}
}
func restartHarness(t *testing.T, h *harness) {
	t.Helper()
	cfg := h.app.cfg
	if err := h.app.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.app = a
	h.handler = a.Handler()
	t.Cleanup(func() { a.Close() })
}
func TestAccountDeletionProfileAndSharedContent(t *testing.T) {
	h := newHarness(t, 4096)
	a := h.account("account_a")
	b := h.account("account_b")
	w := h.request("PATCH", "/api/me", `{"display_name":"测试 <profile>"}`, "application/json", a)
	status(t, w, 200)
	var u User
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Username != "account_a" || u.DisplayName != "测试 <profile>" {
		t.Fatal("profile/login identity changed")
	}
	status(t, h.request("PATCH", "/api/me", `{"username":"stolen"}`, "application/json", a), 400)
	payload := randomContent(t, 1024)
	root := makeFolder(t, h, a, "root", "")
	child := makeFolder(t, h, a, "child", root.ID)
	fa := putFile(t, h, a, "a.bin", child.ID, payload)
	fb := putFile(t, h, b, "b.bin", "", payload)
	putFile(t, h, a, "private.bin", root.ID, []byte("private unique"))
	_, sharedURL := createShareTest(t, h, a, "file", fa.ID)
	session := newSession(t, h, a, "partial.bin", child.ID, 8, 4)
	part(t, h, a, session.ID, 0, []byte("part"), 201)
	status(t, h.request("GET", "/api/folders/"+root.ID+"/download", "", "", a), 200)
	w = h.request("POST", "/api/login", `{"username":"account_a","password":"temporary-test-password"}`, "application/json", nil)
	status(t, w, 200)
	second := w.Result().Cookies()[0]
	restartHarness(t, h)
	w = h.request("GET", "/api/me", "", "", a)
	status(t, w, 200)
	if !bytes.Contains(w.Body.Bytes(), []byte("测试 <profile>")) && !bytes.Contains(w.Body.Bytes(), []byte(`测试 \u003cprofile\u003e`)) {
		t.Fatal("profile lost across restart")
	}
	status(t, h.request("DELETE", "/api/me", "", "", nil), 401)
	status(t, h.request("DELETE", "/api/me", "", "", a), 204)
	status(t, h.request("GET", "/api/me", "", "", a), 401)
	status(t, h.request("GET", "/api/me", "", "", second), 401)
	status(t, h.request("GET", sharedURL, "", "", nil), 404)
	w = h.request("GET", fb.DownloadURL, "", "", b)
	status(t, w, 200)
	sameContent(t, w.Body.Bytes(), payload)
	for _, table := range []string{"folders", "shares", "upload_sessions", "sessions"} {
		if countQuery(t, h.app, "SELECT count(*) FROM "+table+" WHERE user_id=?", u.ID) != 0 {
			t.Fatal("private resource remains", table)
		}
	}
	if entries(t, h.app.blobs) != 1 || entries(t, h.app.temp) != 0 || countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 1 {
		t.Fatal("account storage cleanup incorrect")
	}
	status(t, h.request("POST", "/api/login", `{"username":"account_a","password":"temporary-test-password"}`, "application/json", nil), 401)
}

func httpRange(t *testing.T, client *http.Client, url string, c *http.Cookie, start, end int) ([]byte, int, http.Header) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		req.AddCookie(c)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b, res.StatusCode, res.Header
}
func TestRealHTTPRangeLocalAndAnonymous(t *testing.T) {
	h := newHarness(t, 1<<20)
	c := h.account("range_user")
	payload := randomContent(t, 4096)
	f := putFile(t, h, c, "range.bin", "", payload)
	_, share := createShareTest(t, h, c, "file", f.ID)
	server := httptest.NewServer(h.handler)
	defer server.Close()
	for _, p := range []string{f.DownloadURL, share} {
		cookie := c
		if p == share {
			cookie = nil
		}
		b, code, headers := httpRange(t, server.Client(), server.URL+p, cookie, 0, 99)
		if code != 206 || headers.Get("Content-Range") != "bytes 0-99/4096" || !bytes.Equal(b, payload[:100]) {
			t.Fatal("single range not honored")
		}
		reconstructed := []byte{}
		for start := 0; start < len(payload); start += 700 {
			end := min(start+699, len(payload)-1)
			b, code, _ = httpRange(t, server.Client(), server.URL+p, cookie, start, end)
			if code != 206 {
				t.Fatal("range rejected")
			}
			reconstructed = append(reconstructed, b...)
		}
		sameContent(t, reconstructed, payload)
		_, code, _ = httpRange(t, server.Client(), server.URL+p, cookie, 9000, 9100)
		if code != 416 {
			t.Fatal("invalid range must be 416")
		}
	}
}
func newSession(t *testing.T, h *harness, c *http.Cookie, name, folder string, size, partSize int64) UploadSession {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "folder_id": folder, "expected_size": size, "part_size": partSize})
	w := h.request("POST", "/api/uploads", string(body), "application/json", c)
	status(t, w, 201)
	var s UploadSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func part(t *testing.T, h *harness, c *http.Cookie, id string, index int, b []byte, want int) {
	t.Helper()
	status(t, h.reader("PUT", "/api/uploads/"+id+"/parts/"+strconv.Itoa(index), bytes.NewReader(b), "application/octet-stream", c), want)
}
func TestResumableRestartIdempotenceIsolationCompleteDedup(t *testing.T) {
	h := newHarness(t, 1<<20)
	a := h.account("chunks_a")
	b := h.account("chunks_b")
	payload := randomContent(t, 5000)
	original := putFile(t, h, b, "original.bin", "", payload)
	s := newSession(t, h, a, "resumed.bin", "", 5000, 2048)
	base := "/api/uploads/" + s.ID
	part(t, h, a, s.ID, 0, payload[:2048], 201)
	part(t, h, a, s.ID, 0, payload[:2048], 200)
	part(t, h, a, s.ID, 0, bytes.Repeat([]byte{1}, 2048), 409)
	part(t, h, a, s.ID, 1, payload[2048:4095], 400)
	status(t, h.request("POST", base+"/complete", "", "", a), 409)
	for _, call := range []struct{ method, path string }{{"GET", base}, {"PUT", base + "/parts/1"}, {"POST", base + "/complete"}, {"DELETE", base}} {
		status(t, h.request(call.method, call.path, "", "application/octet-stream", b), 404)
	}
	w := h.request("GET", base, "", "", a)
	status(t, w, 200)
	var progress struct {
		Parts []UploadPart `json:"parts"`
	}
	json.Unmarshal(w.Body.Bytes(), &progress)
	if len(progress.Parts) != 1 || progress.Parts[0].Index != 0 {
		t.Fatal("status does not reflect partial upload")
	}
	restartHarness(t, h)
	status(t, h.request("GET", base, "", "", a), 200)
	part(t, h, a, s.ID, 0, payload[:2048], 200)
	part(t, h, a, s.ID, 1, payload[2048:4096], 201)
	part(t, h, a, s.ID, 2, payload[4096:], 201)
	w = h.request("POST", base+"/complete", "", "", a)
	status(t, w, 201)
	f := parseFile(t, w)
	status(t, h.request("GET", base, "", "", a), 404)
	w = h.request("GET", f.DownloadURL, "", "", a)
	status(t, w, 200)
	sameContent(t, w.Body.Bytes(), payload)
	if entries(t, h.app.blobs) != 1 || entries(t, h.app.temp) != 0 || countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 2 {
		t.Fatal("resumable completion bypassed dedup or left parts")
	}
	status(t, h.request("DELETE", "/api/files/"+original.ID, "", "", b), 204)
	sameContent(t, h.request("GET", f.DownloadURL, "", "", a).Body.Bytes(), payload)
	empty := newSession(t, h, a, "empty.bin", "", 0, 4)
	status(t, h.request("POST", "/api/uploads/"+empty.ID+"/complete", "", "", a), 201)
}
func TestUploadCancelExpiryAndCorruptPart(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("cleanup_user")
	for _, expiry := range []bool{false, true} {
		s := newSession(t, h, c, "partial.bin", "", 8, 4)
		part(t, h, c, s.ID, 0, []byte("data"), 201)
		if expiry {
			if _, err := h.app.db.Exec(`UPDATE upload_sessions SET expires_at=0 WHERE id=?`, s.ID); err != nil {
				t.Fatal(err)
			}
			status(t, h.request("GET", "/api/uploads/"+s.ID, "", "", c), 404)
			if err := h.app.cleanupLocked(time.Now()); err != nil {
				t.Fatal(err)
			}
		} else {
			status(t, h.request("DELETE", "/api/uploads/"+s.ID, "", "", c), 204)
		}
		if entries(t, h.app.temp) != 0 || countQuery(t, h.app, `SELECT count(*) FROM upload_parts`) != 0 || countQuery(t, h.app, `SELECT count(*) FROM upload_sessions`) != 0 {
			t.Fatal("part cleanup incomplete")
		}
	}
	s := newSession(t, h, c, "corrupt.bin", "", 4, 4)
	part(t, h, c, s.ID, 0, []byte("data"), 201)
	var key string
	h.app.db.QueryRow(`SELECT storage_key FROM upload_parts WHERE session_id=?`, s.ID).Scan(&key)
	if err := os.WriteFile(filepath.Join(h.app.temp, key), []byte("oops"), 0600); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("POST", "/api/uploads/"+s.ID+"/complete", "", "", c), 409)
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 0 {
		t.Fatal("corrupt upload published")
	}
	status(t, h.request("DELETE", "/api/uploads/"+s.ID, "", "", c), 204)
}

func TestFolderZIPNestedRangeConflictsInvalidationExpiry(t *testing.T) {
	h := newHarness(t, 1<<20)
	c := h.account("zip_user")
	other := h.account("zip_other")
	root := makeFolder(t, h, c, "archive", "")
	child := makeFolder(t, h, c, "nested", root.ID)
	makeFolder(t, h, c, "empty", child.ID)
	content := randomContent(t, 128<<10)
	f := putFile(t, h, c, "data.bin", child.ID, content)
	putFile(t, h, c, "data.bin", child.ID, []byte("duplicate name"))
	putFile(t, h, c, "nested", root.ID, []byte("file-directory conflict"))
	putFile(t, h, c, "outside.bin", "", []byte("exclude"))
	url := "/api/folders/" + root.ID + "/download"
	status(t, h.request("GET", url, "", "", other), 404)
	w := h.request("GET", url, "", "", c)
	status(t, w, 200)
	archive := append([]byte(nil), w.Body.Bytes()...)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	found := false
	for _, entry := range reader.File {
		if !safeZIPPath(entry.Name) || seen[entry.Name] {
			t.Fatal("unsafe or duplicate zip entry")
		}
		seen[entry.Name] = true
		stream, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(stream)
		stream.Close()
		if e != nil {
			t.Fatal(e)
		}
		if entry.Name == "nested/data.bin" && bytes.Equal(b, content) {
			found = true
		}
		if bytes.Equal(b, []byte("exclude")) {
			t.Fatal("ZIP escaped folder")
		}
	}
	// ID ordering decides which duplicate owns the original name.
	if !found {
		for _, entry := range reader.File {
			stream, _ := entry.Open()
			b, _ := io.ReadAll(stream)
			stream.Close()
			found = found || bytes.Equal(b, content)
		}
	}
	if !found || !seen["nested/empty/"] {
		t.Fatal("nested content missing")
	}
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 1 {
		t.Fatal("ZIP not cached")
	}
	server := httptest.NewServer(h.handler)
	defer server.Close()
	var reconstructed []byte
	for start := 0; start < len(archive); start += 16384 {
		end := min(start+16383, len(archive)-1)
		b, code, headers := httpRange(t, server.Client(), server.URL+url, c, start, end)
		if code != 206 || headers.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end, len(archive)) {
			t.Fatal("ZIP Range incorrect")
		}
		reconstructed = append(reconstructed, b...)
	}
	sameContent(t, reconstructed, archive)
	var oldKey string
	h.app.db.QueryRow(`SELECT storage_key FROM zip_cache`).Scan(&oldKey)
	status(t, h.request("PATCH", "/api/files/"+f.ID, `{"name":"renamed.bin"}`, "application/json", c), 200)
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 0 {
		t.Fatal("stale ZIP not invalidated")
	}
	if _, err = os.Stat(filepath.Join(h.app.temp, oldKey)); !os.IsNotExist(err) {
		t.Fatal("invalidated ZIP retained")
	}
	w = h.request("GET", url, "", "", c)
	status(t, w, 200)
	if bytes.Equal(archive, w.Body.Bytes()) {
		t.Fatal("ZIP did not change after rename")
	}
	if _, err = h.app.db.Exec(`UPDATE zip_cache SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if err = h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if entries(t, h.app.temp) != 0 {
		t.Fatal("expired ZIP retained")
	}
	for _, bad := range []string{"../evil", "/absolute", "C:/escape", `a\b`, "a/../b", ".."} {
		if safeZIPPath(bad) {
			t.Fatal("Zip Slip path accepted")
		}
	}
	// A malicious/corrupt stored name fails closed without publishing a cache.
	if _, err = h.app.db.Exec(`UPDATE files SET name='../escape' WHERE id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("GET", url, "", "", c), 500)
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 0 {
		t.Fatal("unsafe ZIP published")
	}
}
