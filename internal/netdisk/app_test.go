package netdisk

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type harness struct {
	t       *testing.T
	app     *App
	handler http.Handler
}

func newHarness(t *testing.T, limit int64) *harness {
	t.Helper()
	a, err := New(Config{DataDir: t.TempDir(), Origin: "http://127.0.0.1:38120", MaxUploadBytes: limit, BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return &harness{t, a, a.Handler()}
}
func (h *harness) request(method, path, body, kind string, cookie *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.reader(method, path, strings.NewReader(body), kind, cookie)
}
func (h *harness) reader(method, path string, body io.Reader, kind string, cookie *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Content-Type", kind)
	r.Header.Set("X-NetDisk-Request", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, want, w.Body.String())
	}
}
func (h *harness) account(name string) *http.Cookie {
	h.t.Helper()
	body := `{"username":"` + name + `","password":"temporary-test-password"}`
	status(h.t, h.request("POST", "/api/register", body, "application/json", nil), 201)
	w := h.request("POST", "/api/login", body, "application/json", nil)
	status(h.t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		h.t.Fatal("expected one session cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge != 86400 {
		h.t.Fatal("incorrect local cookie attributes")
	}
	if strings.Contains(w.Body.String(), c.Value) || strings.Contains(w.Body.String(), "password") {
		h.t.Fatal("credentials leaked in response body")
	}
	return c
}
func parseFile(t *testing.T, w *httptest.ResponseRecorder) File {
	t.Helper()
	var f File
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func files(t *testing.T, w *httptest.ResponseRecorder) []File {
	t.Helper()
	status(t, w, 200)
	var v struct {
		Files []File `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Files
}

func TestAccountSessionLifecycle(t *testing.T) {
	h := newHarness(t, 1<<20)
	c := h.account("alice")
	status(t, h.request("POST", "/api/register", `{"username":"ALICE","password":"temporary-test-password"}`, "application/json", nil), 409)
	status(t, h.request("POST", "/api/login", `{"username":"alice","password":"incorrect-password"}`, "application/json", nil), 401)
	status(t, h.request("POST", "/api/login", `{"username":"missing","password":"incorrect-password"}`, "application/json", nil), 401)
	status(t, h.request("GET", "/api/me", "", "", c), 200)
	var passwordHash, storedToken string
	var expires int64
	if err := h.app.db.QueryRow(`SELECT password_hash FROM users WHERE username='alice'`).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash == "temporary-test-password" || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("temporary-test-password")) != nil {
		t.Fatal("password not bcrypt hashed")
	}
	if err := h.app.db.QueryRow(`SELECT token_hash,expires_at FROM sessions`).Scan(&storedToken, &expires); err != nil {
		t.Fatal(err)
	}
	if storedToken == c.Value || storedToken != tokenHash(c.Value) || expires <= time.Now().Unix() {
		t.Fatal("session not hashed or not expiring")
	}
	status(t, h.request("POST", "/api/logout", "", "", c), 204)
	// Replaying the old cookie proves server-side revocation, independently of cookie clearing.
	status(t, h.request("GET", "/api/me", "", "", c), 401)
	var count int
	if err := h.app.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("logout retained a session")
	}
	w := h.request("POST", "/api/login", `{"username":"alice","password":"temporary-test-password"}`, "application/json", nil)
	status(t, w, 200)
	fresh := w.Result().Cookies()[0]
	if _, err := h.app.db.Exec(`UPDATE sessions SET expires_at=?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("GET", "/api/me", "", "", fresh), 401)
}

func TestFileLifecycleAndOwnership(t *testing.T) {
	h := newHarness(t, 2<<20)
	a := h.account("user_a")
	b := h.account("user_b")
	payload := bytes.Repeat([]byte{0, 1, 2, 127, 255, 'x'}, 12000)
	w := h.reader("POST", "/api/files?name="+url.QueryEscape("原始.html"), bytes.NewReader(payload), "application/octet-stream", a)
	status(t, w, 201)
	f := parseFile(t, w)
	if f.Size != int64(len(payload)) || f.Name != "原始.html" || !keyPattern.MatchString(f.ID) {
		t.Fatal("incorrect upload metadata")
	}
	listing := files(t, h.request("GET", "/api/files", "", "", a))
	if len(listing) != 1 || listing[0].DownloadURL != "/api/files/"+f.ID+"/download" {
		t.Fatal("incorrect list or link")
	}
	if len(files(t, h.request("GET", "/api/files", "", "", b))) != 0 {
		t.Fatal("B can list A's file")
	}
	path := "/api/files/" + f.ID
	for _, cookie := range []*http.Cookie{b, nil} {
		want := 404
		if cookie == nil {
			want = 401
		}
		for _, call := range []struct{ method, path, body string }{{"GET", path, ""}, {"GET", f.DownloadURL, ""}, {"PATCH", path, `{"name":"stolen.txt"}`}, {"DELETE", path, ""}} {
			status(t, h.request(call.method, call.path, call.body, "application/json", cookie), want)
		}
	}
	for _, p := range []string{"/api/me", "/api/files"} {
		status(t, h.request("GET", p, "", "", nil), 401)
	}
	status(t, h.request("POST", "/api/files?name=x.txt", "x", "application/octet-stream", nil), 401)
	status(t, h.request("POST", "/api/logout", "", "", nil), 401)
	w = h.request("GET", f.DownloadURL, "", "", a)
	status(t, w, 200)
	if sha256.Sum256(w.Body.Bytes()) != sha256.Sum256(payload) {
		t.Fatal("download changed bytes")
	}
	if w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe download headers")
	}
	status(t, h.request("PATCH", path, `{"name":"renamed.bin"}`, "application/json", a), 200)
	w = h.request("GET", f.DownloadURL, "", "", a)
	status(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("rename changed content")
	}
	var key string
	if err := h.app.db.QueryRow(`SELECT storage_key FROM files WHERE id=?`, f.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("DELETE", path, "", "", a), 204)
	status(t, h.request("GET", f.DownloadURL, "", "", a), 404)
	if _, err := os.Stat(filepath.Join(h.app.blobs, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete retained blob")
	}
	if len(files(t, h.request("GET", "/api/files", "", "", a))) != 0 {
		t.Fatal("delete retained accessible record")
	}
	var count int
	if err := h.app.db.QueryRow(`SELECT count(*) FROM files`).Scan(&count); err != nil || count != 0 {
		t.Fatal("delete retained database record")
	}
}

func TestCSRFAndInvalidInput(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("csrfuser")
	for _, headers := range []map[string]string{{}, {"X-NetDisk-Request": "1", "Origin": "https://attacker.example"}, {"X-NetDisk-Request": "1", "Sec-Fetch-Site": "cross-site"}} {
		r := httptest.NewRequest("POST", "/api/files?name=x.txt", strings.NewReader("x"))
		r.AddCookie(c)
		r.Header.Set("Content-Type", "application/octet-stream")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		status(t, w, 403)
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("cross-origin access granted")
		}
	}
	status(t, h.request("OPTIONS", "/api/files", "", "", nil), 405)
	for _, name := range []string{"../escape", "..", ".", "/etc/passwd", `C:\escape`, "a/b", "a\\b", "a\x00b", "a\nb", "", strings.Repeat("a", 256)} {
		status(t, h.request("POST", "/api/files?name="+url.QueryEscape(name), "x", "application/octet-stream", c), 400)
	}
	w := h.request("POST", "/api/files?name=safe.txt", "safe", "application/octet-stream", c)
	status(t, w, 201)
	f := parseFile(t, w)
	status(t, h.request("PATCH", "/api/files/"+f.ID, `{"name":"../escape"}`, "application/json", c), 400)
	status(t, h.request("GET", "/api/files/%2e%2e%2fescape/download", "", "", c), 404)
	entries, err := os.ReadDir(h.app.blobs)
	if err != nil || len(entries) != 1 || !keyPattern.MatchString(entries[0].Name()) {
		t.Fatal("storage key is client controlled")
	}
	if _, err = os.Stat(filepath.Join(h.app.cfg.DataDir, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("escaped storage")
	}
	status(t, h.request("POST", "/api/register", `{"username":"bad","password":"short"}`, "application/json", nil), 400)
	status(t, h.request("POST", "/api/register", `{"username":"valid","password":"temporary-test-password","extra":1}`, "application/json", nil), 400)
	status(t, h.request("POST", "/api/login", strings.Repeat("x", 5000), "application/json", nil), 400)
}

type generatedReader struct {
	remaining  int64
	read       int64
	maxRequest int
	inspect    func()
}

func (g *generatedReader) Read(p []byte) (int, error) {
	if len(p) > g.maxRequest {
		g.maxRequest = len(p)
	}
	if g.inspect != nil && g.read > 128<<10 {
		g.inspect()
		g.inspect = nil
	}
	if g.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > g.remaining {
		n = int(g.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = byte((g.read + int64(i)) % 251)
	}
	g.remaining -= int64(n)
	g.read += int64(n)
	return n, nil
}

type brokenReader struct{ sent bool }

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, []byte("incomplete")), nil
}
func TestStreamingLimitsAndAbortCleanup(t *testing.T) {
	h := newHarness(t, 4<<20)
	c := h.account("streamer")
	observed := false
	body := &generatedReader{remaining: 3 << 20}
	body.inspect = func() {
		entries, err := os.ReadDir(h.app.temp)
		if err != nil || len(entries) != 1 {
			t.Fatal("upload not streamed to temp storage")
		}
		info, err := entries[0].Info()
		if err != nil || info.Size() == 0 {
			t.Fatal("no bytes on disk before body EOF")
		}
		var count int
		if err := h.app.db.QueryRow(`SELECT count(*) FROM files`).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial record published")
		}
		observed = true
	}
	w := h.reader("POST", "/api/files?name=stream.bin", body, "application/octet-stream", c)
	status(t, w, 201)
	if !observed || body.maxRequest > 64<<10 {
		t.Fatalf("streaming proof missing; max read request=%d", body.maxRequest)
	}
	t.Logf("stream proof: %d bytes, maximum reader buffer %d bytes, temp file observed before EOF", body.read, body.maxRequest)
	f := parseFile(t, w)
	status(t, h.request("DELETE", "/api/files/"+f.ID, "", "", c), 204)
	status(t, h.reader("POST", "/api/files?name=aborted.bin", &brokenReader{}, "application/octet-stream", c), 400)
	status(t, h.reader("POST", "/api/files?name=too-big.bin", &generatedReader{remaining: (4 << 20) + 1}, "application/octet-stream", c), 413)
	// Known Content-Length is rejected before creating a temporary file as well.
	status(t, h.request("POST", "/api/files?name=known-big.bin", strings.Repeat("x", (4<<20)+1), "application/octet-stream", c), 413)
	if len(files(t, h.request("GET", "/api/files", "", "", c))) != 0 {
		t.Fatal("failed upload created metadata")
	}
	for _, dir := range []string{h.app.temp, h.app.blobs} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("failed upload left stored content")
		}
	}
}

func TestRestartPersistenceAndPendingDeletionRecovery(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("persistent")
	w := h.request("POST", "/api/files?name=kept.txt", "persisted bytes", "application/octet-stream", c)
	status(t, w, 201)
	f := parseFile(t, w)
	w = h.request("POST", "/api/files?name=pending.txt", "deleted bytes", "application/octet-stream", c)
	status(t, w, 201)
	pending := parseFile(t, w)
	if _, err := h.app.db.Exec(`UPDATE files SET deleted=1 WHERE id=?`, pending.ID); err != nil {
		t.Fatal(err)
	}
	cfg := h.app.cfg
	if err := h.app.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h.app = a
	h.handler = a.Handler()
	status(t, h.request("GET", "/api/me", "", "", c), 200)
	w = h.request("GET", f.DownloadURL, "", "", c)
	status(t, w, 200)
	if w.Body.String() != "persisted bytes" {
		t.Fatal("restart changed content")
	}
	status(t, h.request("POST", "/api/login", `{"username":"persistent","password":"temporary-test-password"}`, "application/json", nil), 200)
	status(t, h.request("GET", pending.DownloadURL, "", "", c), 404)
	entries, err := os.ReadDir(a.blobs)
	if err != nil || len(entries) != 1 {
		t.Fatal("pending deletion not recovered")
	}
}

func TestCookieOriginConfiguration(t *testing.T) {
	for _, cfg := range []Config{{Origin: "http://127.0.0.1:38120", CookieSecure: true}, {Origin: "http://example.com"}, {Origin: "https://example.com", CookieSecure: false}} {
		cfg.DataDir = t.TempDir()
		cfg.MaxUploadBytes = 1024
		cfg.BcryptCost = bcrypt.MinCost
		if a, err := New(cfg); err == nil {
			a.Close()
			t.Fatal("accepted unsafe or unusable cookie configuration")
		}
	}
	a, err := New(Config{DataDir: t.TempDir(), Origin: "https://example.com", CookieSecure: true, MaxUploadBytes: 1024, BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := &harness{t, a, a.Handler()}
	body := `{"username":"secureuser","password":"temporary-test-password"}`
	status(t, h.request("POST", "/api/register", body, "application/json", nil), 201)
	w := h.request("POST", "/api/login", body, "application/json", nil)
	status(t, w, 200)
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS cookie lacks Secure")
	}
}
