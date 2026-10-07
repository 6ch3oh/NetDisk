package netdisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// All filesystem/metadata transitions run under App.mu (one instance per data
// directory). SQLite transactions publish references and durable cleanup work.
func addColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, nn, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || name == column
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}
func (a *App) migrateAdvanced() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = addColumn(tx, "users", "display_name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS blobs(
 id TEXT PRIMARY KEY,content_hash TEXT NOT NULL UNIQUE,size INTEGER NOT NULL CHECK(size>=0),
 storage_key TEXT NOT NULL UNIQUE,backend TEXT NOT NULL DEFAULT 'local' CHECK(backend IN ('local','s3')),
 ref_count INTEGER NOT NULL CHECK(ref_count>=0));
 CREATE TABLE IF NOT EXISTS local_cleanup(storage_key TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS object_cleanup(storage_key TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS shares(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL UNIQUE,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 file_id TEXT REFERENCES files(id) ON DELETE CASCADE,folder_id TEXT REFERENCES folders(id) ON DELETE CASCADE,
 created_at INTEGER NOT NULL,CHECK((file_id IS NULL)<>(folder_id IS NULL)));
 CREATE TABLE IF NOT EXISTS upload_sessions(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL,folder_id TEXT REFERENCES folders(id) ON DELETE RESTRICT,expected_size INTEGER NOT NULL CHECK(expected_size>=0),
 part_size INTEGER NOT NULL CHECK(part_size>0),expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS upload_parts(session_id TEXT NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
 part_index INTEGER NOT NULL CHECK(part_index>=0),size INTEGER NOT NULL,content_hash TEXT NOT NULL,storage_key TEXT NOT NULL,
 PRIMARY KEY(session_id,part_index));
 CREATE TABLE IF NOT EXISTS temp_cleanup(storage_key TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS zip_cache(cache_key TEXT PRIMARY KEY,storage_key TEXT NOT NULL,user_id INTEGER NOT NULL,
 folder_id TEXT NOT NULL,expires_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS uploads_expiry ON upload_sessions(expires_at);`)
	if err != nil {
		return err
	}
	if err = addColumn(tx, "files", "blob_id", "TEXT REFERENCES blobs(id)"); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id,storage_key,size,deleted FROM files WHERE blob_id IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	type legacy struct {
		id, key string
		size    int64
		deleted int
	}
	var all []legacy
	for rows.Next() {
		var f legacy
		if err = rows.Scan(&f.id, &f.key, &f.size, &f.deleted); err != nil {
			rows.Close()
			return err
		}
		all = append(all, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range all {
		if !keyPattern.MatchString(f.key) {
			return errors.New("invalid legacy storage key")
		}
		source, e := os.Open(filepath.Join(a.blobs, f.key))
		if errors.Is(e, os.ErrNotExist) && f.deleted == 1 {
			if _, err = tx.Exec(`DELETE FROM files WHERE id=?`, f.id); err != nil {
				return err
			}
			continue
		}
		if e != nil {
			return e
		}
		h := sha256.New()
		size, e := io.CopyBuffer(h, source, make([]byte, 32*1024))
		source.Close()
		if e != nil {
			return e
		}
		if size != f.size {
			return errors.New("legacy blob size mismatch; migration refused")
		}
		hash := hex.EncodeToString(h.Sum(nil))
		var id, key string
		var existingSize int64
		e = tx.QueryRow(`SELECT id,storage_key,size FROM blobs WHERE content_hash=?`, hash).Scan(&id, &key, &existingSize)
		if errors.Is(e, sql.ErrNoRows) {
			id, e = randomKey(16)
			if e != nil {
				return e
			}
			key = f.key
			_, e = tx.Exec(`INSERT INTO blobs(id,content_hash,size,storage_key,ref_count) VALUES(?,?,?,?,0)`, id, hash, size, key)
		} else if e == nil && existingSize != size {
			return errors.New("hash/size mismatch")
		}
		if e != nil {
			return e
		}
		if _, err = tx.Exec(`UPDATE files SET blob_id=? WHERE id=?`, id, f.id); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE blobs SET ref_count=ref_count+1 WHERE id=?`, id); err != nil {
			return err
		}
		if key != f.key {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO local_cleanup VALUES(?)`, f.key); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS files_blob ON files(blob_id); PRAGMA user_version=2;`); err != nil {
		return err
	}
	for _, table := range []string{"files", "folders"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			row := "NEW"
			if op == "DELETE" {
				row = "OLD"
			}
			_, err = tx.Exec("CREATE TRIGGER IF NOT EXISTS zip_invalidate_" + table + "_" + op + " AFTER " + op + " ON " + table + " BEGIN INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM zip_cache WHERE user_id=" + row + ".user_id; DELETE FROM zip_cache WHERE user_id=" + row + ".user_id; END;")
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (a *App) publish(ctx context.Context, owner int64, name, folder string, size int64, hash, temp string, sessionIDs ...string) (File, error) {
	return a.publishReplacing(ctx, owner, name, folder, size, hash, temp, "", sessionIDs...)
}

func (a *App) publishReplacing(ctx context.Context, owner int64, name, folder string, size int64, hash, temp, replaceID string, sessionIDs ...string) (File, error) {
	var f File
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	if err = checkParent(ctx, tx, owner, folder); err != nil {
		return f, err
	}
	var oldBlob string
	var oldSize int64
	if replaceID != "" {
		if err = tx.QueryRowContext(ctx, `SELECT blob_id,size FROM files WHERE id=? AND user_id=? AND deleted=0`, replaceID, owner).Scan(&oldBlob, &oldSize); err != nil {
			return f, err
		}
	}
	except := ""
	if len(sessionIDs) > 0 {
		except = sessionIDs[0]
	}
	if err = a.checkQuota(ctx, tx, owner, size-oldSize, except); err != nil {
		return f, err
	}
	id, err := randomKey(16)
	if err != nil {
		return f, err
	}
	if replaceID != "" {
		id = replaceID
	}
	key, err := randomKey(16)
	if err != nil {
		return f, err
	}
	var blobID, storageKey string
	var storedSize int64
	var refs int64
	err = tx.QueryRowContext(ctx, `SELECT id,storage_key,size,ref_count FROM blobs WHERE content_hash=?`, hash).Scan(&blobID, &storageKey, &storedSize, &refs)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		blobID, err = randomKey(16)
		if err != nil {
			return f, err
		}
		storageKey = key
		if err = os.Rename(temp, filepath.Join(a.blobs, key)); err != nil {
			return f, err
		}
		created = true
		_, err = tx.ExecContext(ctx, `INSERT INTO blobs(id,content_hash,size,storage_key,ref_count) VALUES(?,?,?,?,0)`, blobID, hash, size, key)
	} else if err == nil && refs == 0 {
		return f, &folderProblem{409, "blob cleanup pending; retry after cleanup"}
	} else if err == nil && storedSize != size {
		return f, errors.New("hash/size mismatch")
	}
	// A failed transaction must not leave an effective unreferenced physical copy.
	committed := false
	defer func() {
		if created && !committed {
			os.Remove(filepath.Join(a.blobs, key))
		}
	}()
	if err != nil {
		return f, err
	}
	f = File{ID: id, Name: name, Size: size, CreatedAt: time.Now().Unix(), FolderID: folder}
	link(&f)
	if replaceID == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO files(id,user_id,name,storage_key,size,created_at,folder_id,blob_id) VALUES(?,?,?,?,?,?,?,?)`, id, owner, name, key, size, f.CreatedAt, nullableID(folder), blobID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE files SET name=?,storage_key=?,size=?,created_at=?,nfs_mtime=?,folder_id=?,blob_id=? WHERE id=? AND user_id=?`, name, key, size, f.CreatedAt, f.CreatedAt, nullableID(folder), blobID, id, owner)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE blobs SET ref_count=ref_count-1 WHERE id=? AND ref_count>0`, oldBlob)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE blobs SET ref_count=ref_count+1 WHERE id=?`, blobID)
	}
	if err == nil && len(sessionIDs) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE upload_requests SET file_id=? WHERE session_id=? AND user_id=?`, f.ID, sessionIDs[0], owner)
	}
	if err == nil && len(sessionIDs) > 0 {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM upload_parts WHERE session_id=?`, sessionIDs[0])
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE id=? AND user_id=?`, sessionIDs[0], owner)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	committed = err == nil
	return f, err
}

// Shared by ordinary deletion, recovery, and account deletion. The reference
// decrement and metadata removal commit together; ref_count=0 is a GC tombstone.
func releaseFile(tx *sql.Tx, id string) error {
	var blob string
	err := tx.QueryRow(`SELECT blob_id FROM files WHERE id=?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM files WHERE id=?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE blobs SET ref_count=ref_count-1 WHERE id=? AND ref_count>0`, blob)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("invalid blob reference count")
	}
	return nil
}
func (a *App) gcBlobs(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx, `SELECT id,storage_key,backend FROM blobs WHERE ref_count=0`)
	if err != nil {
		return err
	}
	type item struct{ id, key, backend string }
	var all []item
	for rows.Next() {
		var p item
		if err = rows.Scan(&p.id, &p.key, &p.backend); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		if !keyPattern.MatchString(p.key) {
			return errors.New("invalid blob key")
		}
		if p.backend == "s3" {
			if a.store == nil {
				return errors.New("object storage required for pending cleanup")
			}
			err = a.store.remove(ctx, p.key)
		} else {
			err = os.Remove(filepath.Join(a.blobs, p.key))
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		if err != nil {
			return err
		}
		if _, err = a.db.ExecContext(ctx, `DELETE FROM blobs WHERE id=? AND ref_count=0`, p.id); err != nil {
			return err
		}
	}
	if err = a.cleanupKeys(ctx, "local_cleanup", a.blobs); err != nil {
		return err
	}
	return a.cleanupObjects(ctx)
}
func (a *App) cleanupKeys(ctx context.Context, table, dir string) error {
	rows, err := a.db.QueryContext(ctx, "SELECT storage_key FROM "+table)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !keyPattern.MatchString(key) {
			return errors.New("invalid cleanup key")
		}
		err = os.Remove(filepath.Join(dir, key))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err = a.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE storage_key=?", key); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) cleanupObjects(ctx context.Context) error {
	if a.store == nil {
		return nil
	} // Pure-local startup remains possible after an aborted migration.
	rows, err := a.db.QueryContext(ctx, `SELECT c.storage_key FROM object_cleanup c LEFT JOIN blobs b ON b.storage_key=c.storage_key WHERE b.id IS NULL OR b.backend='local'`)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !keyPattern.MatchString(key) {
			return errors.New("invalid object cleanup key")
		}
		if err = a.store.remove(ctx, key); err != nil {
			return err
		}
		if _, err = a.db.ExecContext(ctx, `DELETE FROM object_cleanup WHERE storage_key=?`, key); err != nil {
			return err
		}
	}
	return nil
}
