package netdisk

import (
	"embed"
	"net/http"
)

//go:embed web/index.html web/app.css web/app.js web/upload-queue.js web/selection.js
var webAssets embed.FS

func (a *App) webRoutes(m *http.ServeMux) {
	for route, asset := range map[string]string{"/{$}": "index.html", "/assets/app.css": "app.css", "/assets/app.js": "app.js", "/assets/upload-queue.js": "upload-queue.js", "/assets/selection.js": "selection.js"} {
		m.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			body, err := webAssets.ReadFile("web/" + asset)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			types := map[string]string{"index.html": "text/html; charset=utf-8", "app.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "upload-queue.js": "text/javascript; charset=utf-8", "selection.js": "text/javascript; charset=utf-8"}
			w.Header().Set("Content-Type", types[asset])
			// Only bundled same-origin assets run. API/download sandbox stays unchanged.
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			w.Write(body)
		})
	}
	m.Handle("GET /api/config", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]int64{"max_upload_bytes": a.cfg.MaxUploadBytes, "chunk_threshold_bytes": automaticChunkThreshold, "chunk_part_bytes": automaticChunkPartSize})
	})))
}
