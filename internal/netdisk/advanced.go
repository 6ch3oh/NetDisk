package netdisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"
)

func (a *App) advancedRoutes(m *http.ServeMux) {
	for pattern, h := range map[string]http.HandlerFunc{
		"PATCH /api/me": a.profile, "DELETE /api/me": a.deleteAccount,
		"POST /api/shares": a.createShare, "GET /api/shares": a.listShares, "DELETE /api/shares/{id}": a.revokeShare,
		"POST /api/uploads": a.createUpload, "GET /api/uploads/{id}": a.uploadStatus,
		"PUT /api/uploads/{id}/parts/{index}": a.uploadPart, "POST /api/uploads/{id}/complete": a.completeUpload, "DELETE /api/uploads/{id}": a.cancelUpload,
		"GET /api/folders/{id}/download": a.folderZIP, "POST /api/files/{id}/migrate-s3": a.migrateObject,
		"GET /api/download-selection": a.selectionZIP, "DELETE /api/folders/{id}/tree": a.deleteFolderTree,
	} {
		m.Handle(pattern, a.auth(h))
	}
	m.HandleFunc("GET /s/{token}", a.shared)
	m.HandleFunc("GET /s/{token}/files/{id}/download", a.sharedDownload)
}
func (a *App) profile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !utf8.ValidString(body.DisplayName) || utf8.RuneCountInString(body.DisplayName) > 80 {
		fail(w, 400, "invalid display name")
		return
	}
	for _, c := range body.DisplayName {
		if unicode.IsControl(c) {
			fail(w, 400, "invalid display name")
			return
		}
	}
	u := current(r).user
	res, err := a.db.ExecContext(r.Context(), `UPDATE users SET display_name=? WHERE id=?`, body.DisplayName, u.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 401, "authentication required")
		return
	}
	u.DisplayName = body.DisplayName
	respond(w, 200, u)
}
func (a *App) deleteAccount(w http.ResponseWriter, r *http.Request) {
	// Confirmation belongs to the client; authentication, ownership, and CSRF are
	// enforced by the server, with no client-selectable user identifier.
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM files WHERE user_id=?`, owner)
	if err != nil {
		fileError(w, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			fileError(w, err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	for _, id := range ids {
		if err != nil {
			break
		}
		err = releaseFile(tx, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM upload_parts WHERE session_id IN (SELECT id FROM upload_sessions WHERE user_id=?)`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM shares WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE folders SET parent_id=NULL WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM folders WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM users WHERE id=?`, owner)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure, MaxAge: -1})
	if err = a.cleanupLocked(time.Now()); err != nil {
		fail(w, 503, "account removed; private storage cleanup pending")
		return
	}
	w.WriteHeader(204)
}

type Share struct {
	ID        string `json:"id"`
	FileID    string `json:"file_id,omitempty"`
	FolderID  string `json:"folder_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	Type      string `json:"type"`
}

func (a *App) createShare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"resource_type"`
		ID   string `json:"resource_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	owner := current(r).user.ID
	var err error
	switch body.Type {
	case "file", "p2p":
		var id string
		err = a.db.QueryRowContext(r.Context(), `SELECT id FROM files WHERE id=? AND user_id=? AND deleted=0`, body.ID, owner).Scan(&id)
	case "folder":
		_, err = getFolder(r.Context(), a.db, owner, body.ID)
	default:
		fail(w, 400, "resource_type must be file or folder")
		return
	}
	if err != nil {
		folderError(w, err)
		return
	}
	token, err := randomKey(32)
	if err != nil {
		fileError(w, err)
		return
	}
	id, err := randomKey(16)
	if err != nil {
		fileError(w, err)
		return
	}
	s := Share{ID: id, CreatedAt: time.Now().Unix(), Type: "download"}
	if body.Type == "p2p" {
		s.Type = "p2p"
	}
	if body.Type == "file" || body.Type == "p2p" {
		s.FileID = body.ID
	} else {
		s.FolderID = body.ID
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO shares(id,token_hash,user_id,file_id,folder_id,created_at,share_type) VALUES(?,?,?,?,?,?,?)`, id, tokenHash(token), owner, nullableID(s.FileID), nullableID(s.FolderID), s.CreatedAt, s.Type)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 201, map[string]any{"share": s, "url": "/s/" + token})
}
func (a *App) listShares(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,COALESCE(file_id,''),COALESCE(folder_id,''),created_at,share_type FROM shares WHERE user_id=? ORDER BY created_at,id`, current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	defer rows.Close()
	all := []Share{}
	for rows.Next() {
		var s Share
		if err = rows.Scan(&s.ID, &s.FileID, &s.FolderID, &s.CreatedAt, &s.Type); err != nil {
			fileError(w, err)
			return
		}
		all = append(all, s)
	}
	if err = rows.Err(); err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"shares": all})
}
func (a *App) revokeShare(w http.ResponseWriter, r *http.Request) {
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM shares WHERE id=? AND user_id=?`, r.PathValue("id"), current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fileError(w, sql.ErrNoRows)
		return
	}
	w.WriteHeader(204)
}
func (a *App) shareScope(r *http.Request) (Share, int64, error) {
	var s Share
	var owner int64
	token := r.PathValue("token")
	if len(token) != 64 {
		return s, 0, sql.ErrNoRows
	}
	err := a.db.QueryRowContext(r.Context(), `SELECT s.id,s.user_id,COALESCE(s.file_id,''),COALESCE(s.folder_id,''),s.created_at,s.share_type FROM shares s WHERE s.token_hash=?
 AND ((s.file_id IS NOT NULL AND EXISTS(SELECT 1 FROM files f WHERE f.id=s.file_id AND f.user_id=s.user_id AND f.deleted=0))
	 OR (s.folder_id IS NOT NULL AND EXISTS(SELECT 1 FROM folders f WHERE f.id=s.folder_id AND f.user_id=s.user_id)))`, tokenHash(token)).Scan(&s.ID, &owner, &s.FileID, &s.FolderID, &s.CreatedAt, &s.Type)
	return s, owner, err
}
func (a *App) within(ctx context.Context, owner int64, root, id string) error {
	if id == "" {
		return sql.ErrNoRows
	}
	var n int
	err := a.db.QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT id FROM folders WHERE id=? AND user_id=? UNION SELECT f.id FROM folders f JOIN tree t ON f.parent_id=t.id WHERE f.user_id=?) SELECT count(*) FROM tree WHERE id=?`, root, owner, owner, id).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (a *App) shareFile(ctx context.Context, s Share, owner int64, id string) (File, string, error) {
	var f File
	var key string
	err := a.db.QueryRowContext(ctx, `SELECT f.id,f.name,f.size,f.created_at,COALESCE(f.folder_id,''),b.storage_key FROM files f JOIN blobs b ON f.blob_id=b.id WHERE f.id=? AND f.user_id=? AND f.deleted=0`, id, owner).Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &f.FolderID, &key)
	if err != nil {
		return f, key, err
	}
	if s.FileID != "" {
		if id != s.FileID {
			return f, key, sql.ErrNoRows
		}
	} else {
		if err = a.within(ctx, owner, s.FolderID, f.FolderID); err != nil {
			return f, key, err
		}
	}
	return f, key, nil
}
func (a *App) sharedDownload(w http.ResponseWriter, r *http.Request) {
	s, owner, err := a.shareScope(r)
	if err != nil {
		fileError(w, err)
		return
	}
	if s.Type == "p2p" {
		fileError(w, sql.ErrNoRows)
		return
	}
	f, key, err := a.shareFile(r.Context(), s, owner, r.PathValue("id"))
	if err != nil {
		fileError(w, err)
		return
	}
	if r.Method == "GET" {
		if err = a.recordShare(r.Context(), s.ID, "download"); err != nil {
			fileError(w, err)
			return
		}
	}
	a.serveBlob(w, r, f, key)
}
func (a *App) shared(w http.ResponseWriter, r *http.Request) {
	s, owner, err := a.shareScope(r)
	if err != nil {
		fileError(w, err)
		return
	}
	if s.Type == "p2p" {
		a.signalP2P(w, r, s, owner)
		return
	}
	if s.FileID != "" {
		f, key, e := a.shareFile(r.Context(), s, owner, s.FileID)
		if e != nil {
			fileError(w, e)
			return
		}
		if r.Method == "GET" {
			if e = a.recordShare(r.Context(), s.ID, "download"); e != nil {
				fileError(w, e)
				return
			}
		}
		a.serveBlob(w, r, f, key)
		return
	}
	id := r.URL.Query().Get("folder_id")
	if id == "" {
		id = s.FolderID
	}
	if err = a.within(r.Context(), owner, s.FolderID, id); err != nil {
		fileError(w, err)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,created_at FROM folders WHERE user_id=? AND parent_id=? ORDER BY name,id`, owner, id)
	if err != nil {
		fileError(w, err)
		return
	}
	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.Name, &f.CreatedAt); err != nil {
			rows.Close()
			fileError(w, err)
			return
		}
		folders = append(folders, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fileError(w, err)
		return
	}
	rows, err = a.db.QueryContext(r.Context(), `SELECT id,name,size,created_at FROM files WHERE user_id=? AND folder_id=? AND deleted=0 ORDER BY name,id`, owner, id)
	if err != nil {
		fileError(w, err)
		return
	}
	files := []File{}
	for rows.Next() {
		var f File
		if err = rows.Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt); err != nil {
			rows.Close()
			fileError(w, err)
			return
		}
		f.DownloadURL = "/s/" + r.PathValue("token") + "/files/" + f.ID + "/download"
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fileError(w, err)
		return
	}
	// No ancestor breadcrumbs, storage keys, owner details, or private URLs.
	if r.Method == "GET" {
		if err = a.recordShare(r.Context(), s.ID, "view"); err != nil {
			fileError(w, err)
			return
		}
	}
	respond(w, 200, map[string]any{"folder_id": id, "folders": folders, "files": files})
}

func (a *App) startCleanup() {
	a.stop = make(chan struct{})
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-a.stop:
				return
			case now := <-ticker.C:
				a.mu.Lock()
				_ = a.cleanupLocked(now)
				a.mu.Unlock()
			}
		}
	}()
}
func (a *App) cleanupLocked(now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM upload_parts WHERE session_id IN (SELECT id FROM upload_sessions WHERE expires_at<=?)`, now.Unix())
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM upload_requests WHERE expires_at<=? OR (file_id IS NULL AND NOT EXISTS (SELECT 1 FROM upload_sessions s WHERE s.id=upload_requests.session_id))`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT storage_key FROM zip_cache WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM zip_cache WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM email_codes WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM nfs_exports WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM p2p_offers WHERE expires_at<=?`, now.Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM share_events WHERE created_at<?`, now.Add(-30*24*time.Hour).Unix())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	if err = a.cleanupKeys(ctx, "temp_cleanup", a.temp); err != nil {
		return err
	}
	if err = a.gcBlobs(ctx); err != nil {
		return err
	}
	return a.cleanupOrphans(ctx, now)
}

// JSON is also used to form a deterministic fingerprint for ZIP snapshots.
func fingerprint(v any) string { b, _ := json.Marshal(v); return tokenHash(string(b)) }

var missingUpload = &folderProblem{404, "upload session not found"}
var invalidParts = &folderProblem{409, "missing or invalid upload parts"}
