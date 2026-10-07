package netdisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const automaticChunkThreshold int64 = 300_000_000 // MB uses decimal bytes.
const automaticChunkPartSize int64 = 8 << 20

type UploadSession struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	FolderID     string `json:"folder_id"`
	ExpectedSize int64  `json:"expected_size"`
	PartSize     int64  `json:"part_size"`
	ExpiresAt    int64  `json:"expires_at"`
	File         *File  `json:"file,omitempty"`
}
type UploadPart struct {
	Index int64  `json:"index"`
	Size  int64  `json:"size"`
	Hash  string `json:"sha256"`
	key   string
}

func (a *App) session(ctx context.Context, owner int64, id string) (UploadSession, error) {
	var s UploadSession
	if !keyPattern.MatchString(id) {
		return s, missingUpload
	}
	err := a.db.QueryRowContext(ctx, `SELECT id,name,COALESCE(folder_id,''),expected_size,part_size,expires_at FROM upload_sessions WHERE id=? AND user_id=? AND expires_at>?`, id, owner, time.Now().Unix()).Scan(&s.ID, &s.Name, &s.FolderID, &s.ExpectedSize, &s.PartSize, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = missingUpload
	}
	return s, err
}
func (a *App) createUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		FolderID   string `json:"folder_id"`
		Size       int64  `json:"expected_size"`
		PartSize   int64  `json:"part_size"`
		RequestKey string `json:"request_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validName(body.Name) || body.Size < 0 || body.Size > a.cfg.MaxUploadBytes || body.PartSize <= 0 || body.PartSize > 8<<20 {
		fail(w, 400, "invalid upload session; part_size must be 1..8388608")
		return
	}
	owner := current(r).user.ID
	if body.RequestKey != "" {
		if !keyPattern.MatchString(body.RequestKey) {
			fail(w, 400, "invalid upload request key")
			return
		}
		var previous UploadSession
		var fileID string
		err := a.db.QueryRowContext(r.Context(), `SELECT session_id,name,folder_id,expected_size,part_size,expires_at,COALESCE(file_id,'') FROM upload_requests WHERE user_id=? AND request_key=? AND expires_at>?`, owner, body.RequestKey, time.Now().Unix()).Scan(&previous.ID, &previous.Name, &previous.FolderID, &previous.ExpectedSize, &previous.PartSize, &previous.ExpiresAt, &fileID)
		if err == nil {
			if previous.Name != body.Name || previous.FolderID != body.FolderID || previous.ExpectedSize != body.Size || previous.PartSize != body.PartSize {
				fail(w, 409, "upload request key reused for different file")
				return
			}
			if fileID != "" {
				f, e := a.completedUpload(r.Context(), owner, previous.ID)
				if e != nil {
					fileError(w, e)
					return
				}
				previous.File = &f
			}
			respond(w, 201, previous)
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			fileError(w, err)
			return
		}
	}
	// Bound metadata growth even for a one-byte part size.
	if body.Size/body.PartSize > 10000 {
		fail(w, 400, "too many upload parts")
		return
	}
	if err := checkParent(r.Context(), a.db, current(r).user.ID, body.FolderID); err != nil {
		folderError(w, err)
		return
	}
	if err := a.checkQuota(r.Context(), a.db, current(r).user.ID, body.Size, ""); err != nil {
		folderError(w, err)
		return
	}
	id, err := randomKey(16)
	if err != nil {
		fileError(w, err)
		return
	}
	s := UploadSession{ID: id, Name: body.Name, FolderID: body.FolderID, ExpectedSize: body.Size, PartSize: body.PartSize, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO upload_sessions(id,user_id,name,folder_id,expected_size,part_size,expires_at) VALUES(?,?,?,?,?,?,?)`, id, owner, s.Name, nullableID(s.FolderID), s.ExpectedSize, s.PartSize, s.ExpiresAt)
	if err == nil && body.RequestKey != "" {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM upload_requests WHERE user_id=? AND request_key=? AND expires_at<=?`, owner, body.RequestKey, time.Now().Unix())
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO upload_requests(user_id,request_key,session_id,name,folder_id,expected_size,part_size,expires_at) VALUES(?,?,?,?,?,?,?,?)`, owner, body.RequestKey, id, s.Name, s.FolderID, s.ExpectedSize, s.PartSize, s.ExpiresAt)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 201, s)
}
func (a *App) parts(ctx context.Context, id string) ([]UploadPart, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT part_index,size,content_hash,storage_key FROM upload_parts WHERE session_id=? ORDER BY part_index`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UploadPart{}
	for rows.Next() {
		var p UploadPart
		if err = rows.Scan(&p.Index, &p.Size, &p.Hash, &p.key); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (a *App) uploadStatus(w http.ResponseWriter, r *http.Request) {
	s, err := a.session(r.Context(), current(r).user.ID, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	parts, err := a.parts(r.Context(), s.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"session": s, "parts": parts})
}
func partCount(s UploadSession) int64 {
	n := s.ExpectedSize / s.PartSize
	if s.ExpectedSize%s.PartSize != 0 {
		n++
	}
	return n
}
func partBytes(s UploadSession, index int64) int64 {
	size := s.PartSize
	if remaining := s.ExpectedSize - index*s.PartSize; remaining < size {
		size = remaining
	}
	return size
}
func (a *App) uploadPart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := a.session(ctx, current(r).user.ID, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	index, err := strconv.ParseInt(r.PathValue("index"), 10, 64)
	if err != nil || index < 0 || index >= partCount(s) {
		fail(w, 400, "invalid part index")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/octet-stream" {
		fail(w, 415, "expected application/octet-stream")
		return
	}
	expected := partBytes(s, index)
	r.Body = http.MaxBytesReader(w, r.Body, expected)
	tmp, err := os.CreateTemp(a.temp, "upload-")
	if err != nil {
		fileError(w, err)
		return
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	h := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(tmp, h), r.Body, make([]byte, 32*1024))
	if err != nil || size != expected {
		fail(w, 400, "part size mismatch or incomplete body")
		return
	}
	hash := hex.EncodeToString(h.Sum(nil))
	var existing string
	err = a.db.QueryRowContext(ctx, `SELECT content_hash FROM upload_parts WHERE session_id=? AND part_index=?`, s.ID, index).Scan(&existing)
	if err == nil {
		if existing != hash {
			fail(w, 409, "part already uploaded with different content")
			return
		}
		respond(w, 200, UploadPart{Index: index, Size: size, Hash: hash})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		fileError(w, err)
		return
	}
	if err = tmp.Sync(); err != nil {
		fileError(w, err)
		return
	}
	if err = tmp.Close(); err != nil {
		fileError(w, err)
		return
	}
	key, err := randomKey(16)
	if err != nil {
		fileError(w, err)
		return
	}
	path := filepath.Join(a.temp, key)
	if err = os.Rename(tmp.Name(), path); err != nil {
		fileError(w, err)
		return
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO upload_parts(session_id,part_index,size,content_hash,storage_key) VALUES(?,?,?,?,?)`, s.ID, index, size, hash, key)
	if err != nil {
		os.Remove(path)
		fileError(w, err)
		return
	}
	respond(w, 201, UploadPart{Index: index, Size: size, Hash: hash})
}
func (a *App) completeUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := current(r).user.ID
	s, err := a.session(ctx, owner, r.PathValue("id"))
	if errors.Is(err, missingUpload) {
		if f, e := a.completedUpload(ctx, owner, r.PathValue("id")); e == nil {
			respond(w, 201, f)
			return
		} else if !errors.Is(e, sql.ErrNoRows) {
			fileError(w, e)
			return
		}
	}
	if err != nil {
		folderError(w, err)
		return
	}
	parts, err := a.parts(ctx, s.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	if int64(len(parts)) != partCount(s) {
		folderError(w, invalidParts)
		return
	}
	tmp, err := os.CreateTemp(a.temp, "upload-")
	if err != nil {
		fileError(w, err)
		return
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	hash := sha256.New()
	var total int64
	for index, p := range parts {
		if p.Index != int64(index) || p.Size != partBytes(s, p.Index) || !keyPattern.MatchString(p.key) {
			folderError(w, invalidParts)
			return
		}
		source, e := os.Open(filepath.Join(a.temp, p.key))
		if e != nil {
			fileError(w, e)
			return
		}
		partHash := sha256.New()
		size, e := io.CopyBuffer(io.MultiWriter(tmp, hash, partHash), io.LimitReader(source, p.Size+1), make([]byte, 32*1024))
		source.Close()
		if e != nil || size != p.Size || hex.EncodeToString(partHash.Sum(nil)) != p.Hash {
			folderError(w, invalidParts)
			return
		}
		total += size
	}
	if total != s.ExpectedSize {
		folderError(w, invalidParts)
		return
	}
	if err = tmp.Sync(); err != nil {
		fileError(w, err)
		return
	}
	if err = tmp.Close(); err != nil {
		fileError(w, err)
		return
	}
	f, err := a.publish(ctx, owner, s.Name, s.FolderID, total, hex.EncodeToString(hash.Sum(nil)), tmp.Name(), s.ID)
	if err != nil {
		folderError(w, err)
		return
	}
	_ = a.cleanupKeys(ctx, "temp_cleanup", a.temp)
	respond(w, 201, f)
}

// Publish and the completion receipt commit in the same transaction. A response
// lost after publication can be retried without creating another logical file.
func (a *App) completedUpload(ctx context.Context, owner int64, sessionID string) (File, error) {
	var f File
	err := a.db.QueryRowContext(ctx, `SELECT f.id,f.name,f.size,f.created_at,COALESCE(f.folder_id,'') FROM upload_requests u JOIN files f ON u.file_id=f.id WHERE u.session_id=? AND u.user_id=? AND f.user_id=? AND f.deleted=0 AND u.expires_at>?`, sessionID, owner, owner, time.Now().Unix()).Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &f.FolderID)
	link(&f)
	return f, err
}
func (a *App) cancelUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := a.session(ctx, current(r).user.ID, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM upload_parts WHERE session_id=?`, s.ID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM upload_requests WHERE session_id=? AND user_id=? AND file_id IS NULL`, s.ID, current(r).user.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE id=?`, s.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		err = a.cleanupKeys(ctx, "temp_cleanup", a.temp)
	}
	if err != nil {
		fileError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *App) cleanupOrphans(ctx context.Context, now time.Time) error {
	// Only our private directories and server-generated names are eligible.
	for _, dir := range []string{a.temp, a.blobs} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			key := entry.Name()
			if entry.IsDir() || (!keyPattern.MatchString(key) && !strings.HasPrefix(key, "upload-")) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if now.Sub(info.ModTime()) < 24*time.Hour {
				continue
			}
			var n int
			if dir == a.blobs {
				err = a.db.QueryRowContext(ctx, `SELECT count(*) FROM blobs WHERE storage_key=?`, key).Scan(&n)
			} else {
				err = a.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM upload_parts WHERE storage_key=?)+(SELECT count(*) FROM zip_cache WHERE storage_key=?)`, key, key).Scan(&n)
			}
			if err != nil {
				return err
			}
			if n == 0 {
				if err = os.Remove(filepath.Join(dir, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
	}
	return nil
}
