package netdisk

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestAdvancedMigrationRollbackPreservesLegacyContent(t *testing.T) {
	for _, bad := range []string{"size-mismatch", "missing-content"} {
		t.Run(bad, func(t *testing.T) {
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
 CREATE TABLE files(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),name TEXT NOT NULL,storage_key TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,created_at INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0);
 INSERT INTO users VALUES(1,'fixture','synthetic-hash',1);`)
			if err != nil {
				t.Fatal(err)
			}
			content := []byte("legacy content must survive refused migration")
			for i, id := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
				size := len(content)
				if i == 1 && bad == "size-mismatch" {
					size++
				}
				if !(i == 1 && bad == "missing-content") {
					if err = os.WriteFile(filepath.Join(dir, "blobs", id), content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = db.Exec(`INSERT INTO files(id,user_id,name,storage_key,size,created_at) VALUES(?,1,'legacy.txt',?,?,1)`, id, id, size); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			a, err := New(Config{DataDir: dir, Origin: "http://127.0.0.1:38120", MaxUploadBytes: 1024, BcryptCost: bcrypt.MinCost})
			if err == nil {
				a.Close()
				t.Fatal("unsafe migration accepted")
			}
			db, err = sql.Open("sqlite", filepath.Join(dir, "netdisk.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM files`).Scan(&count); err != nil || count != 2 {
				t.Fatal("legacy metadata lost")
			}
			if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='blobs'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("advanced migration not rolled back")
			}
			got, err := os.ReadFile(filepath.Join(dir, "blobs", strings.Repeat("a", 32)))
			if err != nil || !bytes.Equal(got, content) {
				t.Fatal("legacy content modified")
			}
		})
	}
}
func TestPendingReferenceCleanupRecovery(t *testing.T) {
	h := newHarness(t, 4096)
	c := h.account("pending_gc")
	f := putFile(t, h, c, "pending.bin", "", []byte("private synthetic content"))
	// Model a crash after the transactional last-reference release and before GC.
	tx, err := h.app.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = releaseFile(tx, f.ID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 0 || entries(t, h.app.blobs) != 1 {
		t.Fatal("crash fixture invalid")
	}
	restartHarness(t, h)
	if entries(t, h.app.blobs) != 0 || countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 0 {
		t.Fatal("pending blob cleanup not retried")
	}
}
