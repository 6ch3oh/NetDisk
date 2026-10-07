package netdisk

import (
	"regexp"
	"strings"
	"testing"
)

func TestBundledWebAndPolicyIsolation(t *testing.T) {
	h := newHarness(t, 1024)
	for _, p := range []string{"/", "/assets/app.js", "/assets/app.css", "/assets/upload-queue.js", "/assets/selection.js"} {
		w := h.request("GET", p, "", "", nil)
		status(t, w, 200)
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatal("unsafe web CSP")
		}
	}
	status(t, h.request("GET", "/assets/", "", "", nil), 404)
	redirect := h.request("GET", "/assets/../app.go", "", "", nil)
	if redirect.Code < 300 || redirect.Code >= 400 || redirect.Header().Get("Location") != "/app.go" {
		t.Fatal("unexpected path normalization")
	}
	status(t, h.request("GET", "/app.go", "", "", nil), 404)
	w := h.request("GET", "/api/config", "", "", nil)
	status(t, w, 401)
	if w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatal("API sandbox weakened")
	}
	c := h.account("webtester")
	w = h.request("GET", "/api/config", "", "", c)
	status(t, w, 200)
	if !strings.Contains(w.Body.String(), `"max_upload_bytes":1024`) {
		t.Fatal("upload limit not read from backend")
	}
	if !strings.Contains(w.Body.String(), `"chunk_threshold_bytes":300000000`) || !strings.Contains(w.Body.String(), `"chunk_part_bytes":8388608`) {
		t.Fatal("automatic upload policy not supplied by backend")
	}
}

func TestBatchUploadWebAssets(t *testing.T) {
	h := newHarness(t, 1024)
	page := h.request("GET", "/", "", "", nil)
	status(t, page, 200)
	html := page.Body.String()
	for _, obsolete := range []string{"resume-title", "resume-id", "resume-button", "cancel-upload-button"} {
		if strings.Contains(html, obsolete) {
			t.Fatal("manual chunk upload panel still exposed", obsolete)
		}
	}
	if !regexp.MustCompile(`<input\b[^>]*id="upload-input"[^>]*\bmultiple\b`).MatchString(html) {
		t.Fatal("ordinary upload does not allow multiple files")
	}
	queue := strings.Index(html, `src="/assets/upload-queue.js"`)
	app := strings.Index(html, `src="/assets/app.js"`)
	if queue < 0 || app < queue || !strings.Contains(html, `id="upload-list"`) || !strings.Contains(html, `id="upload-status"`) {
		t.Fatal("queue must load before the app and expose per-file/progress feedback")
	}
	asset := h.request("GET", "/assets/upload-queue.js", "", "", nil)
	status(t, asset, 200)
	if asset.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatal("queue must be served as executable same-origin JavaScript")
	}
	stored, err := webAssets.ReadFile("web/upload-queue.js")
	if err != nil || asset.Body.String() != string(stored) {
		t.Fatal("served queue differs from bundled source", err)
	}
}
