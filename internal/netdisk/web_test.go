package netdisk

import (
	"strings"
	"testing"
)

func TestBundledWebAndPolicyIsolation(t *testing.T) {
	h := newHarness(t, 1024)
	for _, p := range []string{"/", "/assets/app.js", "/assets/app.css"} {
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
}
