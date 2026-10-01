package netdisk

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func folderJSON(name, parent string) string {
	b, _ := json.Marshal(map[string]string{"name": name, "parent_id": parent})
	return string(b)
}
func parentJSON(key, id string) string {
	b, _ := json.Marshal(map[string]string{key: id})
	return string(b)
}
func makeFolder(t *testing.T, h *harness, c *http.Cookie, name, parent string) Folder {
	t.Helper()
	w := h.request("POST", "/api/folders", folderJSON(name, parent), "application/json", c)
	status(t, w, 201)
	var f Folder
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestFolderOperationsIsolationAndCycles(t *testing.T) {
	h := newHarness(t, 4096)
	a := h.account("folders_a")
	b := h.account("folders_b")
	parent := makeFolder(t, h, a, "parent", "")
	child := makeFolder(t, h, a, "child", parent.ID)
	foreign := makeFolder(t, h, b, "private", "")
	status(t, h.request("POST", "/api/folders", folderJSON("parent", ""), "application/json", a), 409)
	status(t, h.request("POST", "/api/folders", folderJSON("bad", foreign.ID), "application/json", a), 404)
	status(t, h.request("POST", "/api/folders", folderJSON("../escape", ""), "application/json", a), 400)
	status(t, h.request("GET", "/api/directory?folder_id="+parent.ID, "", "", b), 404)
	status(t, h.request("GET", "/api/folders/"+parent.ID, "", "", b), 404)
	status(t, h.request("GET", "/api/directory", "", "", nil), 401)
	status(t, h.request("POST", "/api/folders/"+parent.ID+"/move", parentJSON("parent_id", parent.ID), "application/json", a), 409)
	status(t, h.request("POST", "/api/folders/"+parent.ID+"/move", parentJSON("parent_id", child.ID), "application/json", a), 409)
	status(t, h.request("POST", "/api/folders/"+parent.ID+"/move", parentJSON("parent_id", foreign.ID), "application/json", a), 404)
	status(t, h.request("POST", "/api/folders/"+parent.ID+"/move", parentJSON("parent_id", ""), "application/json", b), 404)
	status(t, h.request("PATCH", "/api/folders/"+parent.ID, `{"name":"stolen"}`, "application/json", b), 404)
	status(t, h.request("DELETE", "/api/folders/"+parent.ID, "", "", b), 404)
	status(t, h.request("DELETE", "/api/folders/"+parent.ID, "", "", a), 409)
	status(t, h.request("PATCH", "/api/folders/"+child.ID, `{"name":"renamed"}`, "application/json", a), 200)
	w := h.request("POST", "/api/files?name=proof.txt&folder_id="+child.ID, "unchanged content", "application/octet-stream", a)
	status(t, w, 201)
	f := parseFile(t, w)
	status(t, h.request("DELETE", "/api/folders/"+child.ID, "", "", a), 409)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", foreign.ID), "application/json", a), 404)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", ""), "application/json", b), 404)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", strings.Repeat("f", 32)), "application/json", a), 404)
	status(t, h.request("POST", "/api/files?name=forbidden.txt&folder_id="+foreign.ID, "x", "application/octet-stream", a), 404)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", parent.ID), "application/json", a), 204)
	status(t, h.request("POST", "/api/folders/"+child.ID+"/move", parentJSON("parent_id", ""), "application/json", a), 200)
	status(t, h.request("POST", "/api/folders/"+child.ID+"/move", parentJSON("parent_id", parent.ID), "application/json", a), 200)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", ""), "application/json", a), 204)
	root := h.request("GET", "/api/directory", "", "", a)
	status(t, root, 200)
	var listing struct {
		Files   []File   `json:"files"`
		Folders []Folder `json:"folders"`
	}
	json.Unmarshal(root.Body.Bytes(), &listing)
	if len(listing.Files) != 1 || len(listing.Folders) != 1 {
		t.Fatal("root listing incorrect")
	}
	if len(files(t, h.request("GET", "/api/files", "", "", a))) != 1 {
		t.Fatal("S1 all-files meaning changed")
	}
	download := h.request("GET", f.DownloadURL, "", "", a)
	status(t, download, 200)
	if sha256.Sum256(download.Body.Bytes()) != sha256.Sum256([]byte("unchanged content")) {
		t.Fatal("move changed content")
	}
	status(t, h.request("DELETE", "/api/folders/"+child.ID, "", "", a), 204)
	status(t, h.request("POST", "/api/files/"+f.ID+"/move", parentJSON("folder_id", child.ID), "application/json", a), 404)
	status(t, h.request("DELETE", "/api/folders/"+parent.ID, "", "", a), 204)
	for _, method := range []string{"PATCH", "DELETE"} {
		status(t, h.request(method, "/api/folders/root", `{"name":"no"}`, "application/json", a), 404)
	}
}
func TestConcurrentFolderMovesCannotFormCycle(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("concurrent")
	a := makeFolder(t, h, c, "a", "")
	b := makeFolder(t, h, c, "b", "")
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func(p [2]string) {
			defer wg.Done()
			codes <- h.request("POST", "/api/folders/"+p[0]+"/move", parentJSON("parent_id", p[1]), "application/json", c).Code
		}(pair)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent move outcomes %v", counts)
	}
}
func TestLegacyMigrationAndFolderRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "netdisk.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,created_at INTEGER NOT NULL);
 CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),expires_at INTEGER NOT NULL);
 CREATE TABLE files(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),name TEXT NOT NULL,storage_key TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,created_at INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0);`)
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32))
	hash, _ := bcrypt.GenerateFromPassword([]byte("legacy-test-password"), bcrypt.MinCost)
	if _, err = db.Exec(`INSERT INTO users VALUES(1,'legacy',?,?)`, string(hash), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO sessions VALUES(?,1,?)`, tokenHash(token), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	payload := []byte("legacy bytes are preserved")
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	for _, id := range ids {
		if err = os.WriteFile(filepath.Join(dir, "blobs", id), payload, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO files(id,user_id,name,storage_key,size,created_at) VALUES(?,1,'same.txt',?,?,?)`, id, id, len(payload), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := Config{DataDir: dir, Origin: "http://127.0.0.1:38120", MaxUploadBytes: 1024, BcryptCost: bcrypt.MinCost}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t, app, app.Handler()}
	c := &http.Cookie{Name: cookieName, Value: token}
	status(t, h.request("GET", "/api/me", "", "", c), 200)
	original := files(t, h.request("GET", "/api/files", "", "", c))
	if len(original) != 2 || original[0].Name != "same.txt" || original[1].Name != "same.txt" {
		t.Fatal("legacy duplicate names changed")
	}
	root := h.request("GET", "/api/directory", "", "", c)
	status(t, root, 200)
	var listed struct {
		Files []File `json:"files"`
	}
	json.Unmarshal(root.Body.Bytes(), &listed)
	if len(listed.Files) != 2 {
		t.Fatal("old files missing from root")
	}
	folder := makeFolder(t, h, c, "persisted", "")
	child := makeFolder(t, h, c, "child", folder.ID)
	status(t, h.request("POST", "/api/files/"+ids[0]+"/move", parentJSON("folder_id", child.ID), "application/json", c), 204)
	app.Close()
	app, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h.app = app
	h.handler = app.Handler()
	d := h.request("GET", "/api/directory?folder_id="+child.ID, "", "", c)
	status(t, d, 200)
	if !strings.Contains(d.Body.String(), ids[0]) || !strings.Contains(d.Body.String(), "persisted") {
		t.Fatal("directory association/breadcrumbs lost on restart")
	}
	for _, id := range ids {
		w := h.request("GET", "/api/files/"+id+"/download", "", "", c)
		status(t, w, 200)
		if sha256.Sum256(w.Body.Bytes()) != sha256.Sum256(payload) {
			t.Fatal("legacy download changed")
		}
	}
	status(t, h.request("POST", "/api/login", `{"username":"legacy","password":"legacy-test-password"}`, "application/json", nil), 200)
}
