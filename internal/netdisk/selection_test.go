package netdisk

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readSelectionZIP(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{}
	for _, entry := range z.File {
		if !safeZIPPath(entry.Name) {
			t.Fatal("unsafe selection ZIP", entry.Name)
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := contents[entry.Name]; exists {
			t.Fatal("duplicate ZIP entry")
		}
		contents[entry.Name] = b
	}
	return contents
}
func TestSelectionZIPContentsDuplicatesRangeOwnershipAndInvalidation(t *testing.T) {
	h := newHarness(t, 1<<20)
	a, b := h.account("selection_a"), h.account("selection_b")
	root := makeFolder(t, h, a, "chosen", "")
	nested := makeFolder(t, h, a, "nested", root.ID)
	makeFolder(t, h, a, "empty", nested.ID)
	inside := putFile(t, h, a, "inside.txt", nested.ID, []byte("inside"))
	file := putFile(t, h, a, "top.txt", "", []byte("top"))
	putFile(t, h, a, "excluded.txt", "", []byte("exclude"))
	foreign := putFile(t, h, b, "foreign.txt", "", []byte("foreign"))
	url := "/api/download-selection?file_ids=" + file.ID + "," + inside.ID + "," + file.ID + "&folder_ids=" + root.ID + "," + nested.ID
	w := h.request("GET", url, "", "", a)
	status(t, w, 200)
	if w.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("not ZIP")
	}
	contents := readSelectionZIP(t, w.Body.Bytes())
	if len(contents) != 5 || string(contents["chosen/nested/inside.txt"]) != "inside" || string(contents["top.txt"]) != "top" {
		t.Fatal("selection expanded outside scope or duplicated", contents)
	}
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 1 {
		t.Fatal("not cached")
	}
	head := h.request("HEAD", url, "", "", a)
	status(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD sent archive")
	}
	server := httptest.NewServer(h.handler)
	defer server.Close()
	reconstructed := []byte{}
	for start := 0; start < w.Body.Len(); start += 80 {
		data, code, headers := httpRange(t, server.Client(), server.URL+url, a, start, min(start+79, w.Body.Len()-1))
		if code != 206 || !strings.HasPrefix(headers.Get("Content-Range"), "bytes ") {
			t.Fatal("selection Range failed")
		}
		reconstructed = append(reconstructed, data...)
	}
	sameContent(t, reconstructed, w.Body.Bytes())
	status(t, h.request("GET", url, "", "", nil), 401)
	status(t, h.request("GET", url, "", "", b), 404)
	status(t, h.request("GET", "/api/download-selection?file_ids="+file.ID+","+foreign.ID, "", "", a), 404)
	for _, query := range []string{"", "?file_ids=../bad", "?file_ids=" + strings.Repeat("a", 32) + ",", "?folder_ids=not-an-id"} {
		status(t, h.request("GET", "/api/download-selection"+query, "", "", a), 400)
	}
	status(t, h.request("PATCH", "/api/files/"+file.ID, `{"name":"renamed.txt"}`, "application/json", a), 200)
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 0 {
		t.Fatal("archive not invalidated")
	}
	w = h.request("GET", url, "", "", a)
	status(t, w, 200)
	if string(readSelectionZIP(t, w.Body.Bytes())["renamed.txt"]) != "top" {
		t.Fatal("stale selection archive")
	}
	if _, err := h.app.db.Exec(`UPDATE zip_cache SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if err := h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, `SELECT count(*) FROM zip_cache`) != 0 || entries(t, h.app.temp) != 0 {
		t.Fatal("ZIP expiry leaked")
	}
}
func TestSelectionRecursiveDeleteIsolationDedupeChunksAndSharing(t *testing.T) {
	h := newHarness(t, 1024)
	a, b := h.account("delete_tree_a"), h.account("delete_tree_b")
	root := makeFolder(t, h, a, "remove", "")
	child := makeFolder(t, h, a, "child", root.ID)
	one := putFile(t, h, a, "one.bin", root.ID, []byte("shared"))
	putFile(t, h, a, "two.bin", child.ID, []byte("shared"))
	other := putFile(t, h, b, "keep.bin", "", []byte("shared"))
	outside := putFile(t, h, a, "outside.bin", "", []byte("outside"))
	_, link := createShareTest(t, h, a, "folder", root.ID)
	_, fileLink := createShareTest(t, h, a, "file", one.ID)
	s := newSession(t, h, a, "partial.bin", child.ID, 8, 4)
	part(t, h, a, s.ID, 0, []byte("part"), 201)
	status(t, h.request("GET", "/api/folders/"+root.ID+"/download", "", "", a), 200)
	endpoint := "/api/folders/" + root.ID + "/tree"
	status(t, h.request("DELETE", endpoint, "", "", nil), 401)
	status(t, h.request("DELETE", endpoint, "", "", b), 404)
	req := httptest.NewRequest("DELETE", endpoint, nil)
	req.AddCookie(a)
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	status(t, w, 403)
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 4 {
		t.Fatal("rejected delete changed resources")
	}
	status(t, h.request("DELETE", endpoint, "", "", a), 204)
	status(t, h.request("DELETE", endpoint, "", "", a), 404)
	status(t, h.request("GET", "/api/uploads/"+s.ID, "", "", a), 404)
	status(t, h.request("GET", link, "", "", nil), 404)
	status(t, h.request("GET", fileLink, "", "", nil), 404)
	if countQuery(t, h.app, `SELECT count(*) FROM folders`) != 0 || countQuery(t, h.app, `SELECT count(*) FROM upload_parts`) != 0 || entries(t, h.app.temp) != 0 {
		t.Fatal("subtree deletion left metadata/parts/archive")
	}
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 2 || countQuery(t, h.app, `SELECT ref_count FROM blobs WHERE size=6`) != 1 {
		t.Fatal("dedupe reference corruption")
	}
	restartHarness(t, h)
	sameContent(t, h.request("GET", other.DownloadURL, "", "", b).Body.Bytes(), []byte("shared"))
	sameContent(t, h.request("GET", outside.DownloadURL, "", "", a).Body.Bytes(), []byte("outside"))
}
func TestSelectionRecursiveDeleteCleanupFailureIsDurable(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("delete_recovery")
	root := makeFolder(t, h, c, "tree", "")
	putFile(t, h, c, "only.bin", root.ID, []byte("synthetic"))
	var key string
	if err := h.app.db.QueryRow(`SELECT storage_key FROM blobs`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.app.blobs, key)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(path, "synthetic-blocker")
	if err := os.WriteFile(blocker, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	status(t, h.request("DELETE", "/api/folders/"+root.ID+"/tree", "", "", c), 503)
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 0 || countQuery(t, h.app, `SELECT count(*) FROM blobs WHERE ref_count=0`) != 1 {
		t.Fatal("failed GC has no recovery tombstone")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 0 {
		t.Fatal("pending GC did not recover")
	}
}
