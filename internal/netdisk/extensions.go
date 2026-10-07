package netdisk

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math/big"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (a *App) migrateExtensions() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ table, name, def string }{
		{"users", "email", "TEXT NOT NULL DEFAULT ''"},
		{"blobs", "access_count", "INTEGER NOT NULL DEFAULT 0"},
		{"blobs", "last_access", "INTEGER NOT NULL DEFAULT 0"},
		{"shares", "share_type", "TEXT NOT NULL DEFAULT 'download'"},
		{"files", "nfs_mode", "INTEGER NOT NULL DEFAULT 384"},
		{"folders", "nfs_mode", "INTEGER NOT NULL DEFAULT 448"},
		{"files", "nfs_mtime", "INTEGER NOT NULL DEFAULT 0"},
		{"folders", "nfs_mtime", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err = addColumn(tx, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_email ON users(email) WHERE email<>'';
 CREATE TABLE IF NOT EXISTS email_codes(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,email TEXT NOT NULL,code_hash TEXT NOT NULL,expires_at INTEGER NOT NULL,issued_at INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS share_events(id INTEGER PRIMARY KEY,share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,kind TEXT NOT NULL,created_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS events_share ON share_events(share_id,created_at);
 CREATE TABLE IF NOT EXISTS nfs_exports(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,token_hash TEXT NOT NULL UNIQUE,expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS p2p_offers(share_id TEXT PRIMARY KEY REFERENCES shares(id) ON DELETE CASCADE,address TEXT NOT NULL,expires_at INTEGER NOT NULL,fingerprint TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS upload_requests(user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,session_id TEXT NOT NULL UNIQUE,name TEXT NOT NULL,folder_id TEXT NOT NULL,
 expected_size INTEGER NOT NULL,part_size INTEGER NOT NULL,expires_at INTEGER NOT NULL,
 file_id TEXT REFERENCES files(id) ON DELETE CASCADE,PRIMARY KEY(user_id,request_key));
 CREATE INDEX IF NOT EXISTS upload_requests_expiry ON upload_requests(expires_at);`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) extensionRoutes(m *http.ServeMux) {
	for p, h := range map[string]http.HandlerFunc{
		"POST /api/me/email-code": a.emailCode, "POST /api/me/email": a.bindEmail, "POST /api/me/password": a.changePassword,
		"GET /api/me/stats": a.userStats, "GET /api/shares/{id}/analytics": a.shareAnalytics,
		"GET /api/storage/candidates": a.storageCandidates, "POST /api/storage/policy": a.executePolicy,
	} {
		m.Handle(p, a.auth(h))
	}
}
func (a *App) checkQuota(ctx context.Context, q rowQuery, owner, additional int64, except string) error {
	var used, reserved int64
	err := q.QueryRowContext(ctx, `SELECT (SELECT COALESCE(sum(size),0) FROM files WHERE user_id=? AND deleted=0),(SELECT COALESCE(sum(expected_size),0) FROM upload_sessions WHERE user_id=? AND id<>? AND expires_at>?)`, owner, owner, except, time.Now().Unix()).Scan(&used, &reserved)
	if err != nil {
		return err
	}
	if additional > a.cfg.QuotaBytes-used-reserved {
		return &folderProblem{413, "user quota exceeded"}
	}
	return nil
}
func (a *App) userStats(w http.ResponseWriter, r *http.Request) {
	var files, folders, used, reserved, physical int64
	err := a.db.QueryRowContext(r.Context(), `SELECT
 (SELECT count(*) FROM files WHERE user_id=? AND deleted=0),
 (SELECT count(*) FROM folders WHERE user_id=?),
 (SELECT COALESCE(sum(size),0) FROM files WHERE user_id=? AND deleted=0),
 (SELECT COALESCE(sum(expected_size),0) FROM upload_sessions WHERE user_id=? AND expires_at>?),
 (SELECT COALESCE(sum(size),0) FROM blobs WHERE id IN (SELECT blob_id FROM files WHERE user_id=? AND deleted=0))`, current(r).user.ID, current(r).user.ID, current(r).user.ID, current(r).user.ID, time.Now().Unix(), current(r).user.ID).Scan(&files, &folders, &used, &reserved, &physical)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"files": files, "folders": folders, "used_bytes": used, "reserved_bytes": reserved, "unique_content_bytes": physical, "quota_bytes": a.cfg.QuotaBytes, "dev_email": a.cfg.DevEmail})
}
func normalizeEmail(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	e, err := mail.ParseAddress(v)
	if err != nil || e.Address != v || len(v) > 254 || !strings.Contains(v, ".") {
		return "", errors.New("invalid email")
	}
	return v, nil
}
func (a *App) emailCode(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.DevEmail {
		fail(w, 503, "email adapter disabled")
		return
	}
	var b struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &b) {
		return
	}
	email, err := normalizeEmail(b.Email)
	if err != nil {
		fail(w, 400, "invalid email")
		return
	}
	owner := current(r).user.ID
	now := time.Now().Unix()
	var last int64
	err = a.db.QueryRowContext(r.Context(), `SELECT issued_at FROM email_codes WHERE user_id=?`, owner).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fileError(w, err)
		return
	}
	if now-last < 30 {
		fail(w, 429, "wait before requesting another code")
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		fileError(w, err)
		return
	}
	code := strings.Repeat("0", 6-len(n.String())) + n.String()
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO email_codes(user_id,email,code_hash,expires_at,issued_at,attempts) VALUES(?,?,?,?,?,0) ON CONFLICT(user_id) DO UPDATE SET email=excluded.email,code_hash=excluded.code_hash,expires_at=excluded.expires_at,issued_at=excluded.issued_at,attempts=0`, owner, email, tokenHash(code), now+600, now)
	if err != nil {
		fileError(w, err)
		return
	}
	// Intentional authenticated local delivery, never logged or persisted as plaintext.
	respond(w, 201, map[string]any{"dev_code": code, "expires_in": 600, "delivery": "local-demo"})
}
func (a *App) bindEmail(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decode(w, r, &b) {
		return
	}
	email, err := normalizeEmail(b.Email)
	if err != nil {
		fail(w, 400, "invalid email")
		return
	}
	owner := current(r).user.ID
	var storedEmail, hash string
	var expires, attempts int64
	err = a.db.QueryRowContext(r.Context(), `SELECT email,code_hash,expires_at,attempts FROM email_codes WHERE user_id=?`, owner).Scan(&storedEmail, &hash, &expires, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 400, "invalid or expired code")
		return
	}
	if err != nil {
		fileError(w, err)
		return
	}
	if storedEmail != email || expires <= time.Now().Unix() || attempts >= 5 || len(b.Code) != 6 || subtle.ConstantTimeCompare([]byte(hash), []byte(tokenHash(b.Code))) != 1 {
		_, _ = a.db.ExecContext(r.Context(), `UPDATE email_codes SET attempts=attempts+1 WHERE user_id=?`, owner)
		fail(w, 400, "invalid or expired code")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `UPDATE users SET email=? WHERE id=?`, email, owner)
	if err != nil {
		fail(w, 409, "email unavailable")
		return
	}
	_, err = tx.ExecContext(r.Context(), `DELETE FROM email_codes WHERE user_id=?`, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]string{"email": email})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if !decode(w, r, &b) {
		return
	}
	if len(b.New) < 10 || len(b.New) > 72 {
		fail(w, 400, "password must be 10..72 bytes")
		return
	}
	var hash string
	owner := current(r).user.ID
	if err := a.db.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id=?`, owner).Scan(&hash); err != nil {
		fileError(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Old)) != nil {
		fail(w, 403, "current password incorrect")
		return
	}
	next, err := bcrypt.GenerateFromPassword([]byte(b.New), a.cfg.BcryptCost)
	if err != nil {
		fileError(w, err)
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `UPDATE users SET password_hash=? WHERE id=?`, string(next), owner)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM nfs_exports WHERE user_id=?`, owner)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM email_codes WHERE user_id=?`, owner)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure, MaxAge: -1})
	w.WriteHeader(204)
}
func (a *App) recordShare(ctx context.Context, id, kind string) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO share_events(share_id,kind,created_at) VALUES(?,?,?)`, id, kind, time.Now().Unix())
	return err
}
func (a *App) shareAnalytics(w http.ResponseWriter, r *http.Request) {
	var exists int
	if err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM shares WHERE id=? AND user_id=?`, r.PathValue("id"), current(r).user.ID).Scan(&exists); err != nil {
		fileError(w, err)
		return
	}
	var views, downloads, signals int64
	err := a.db.QueryRowContext(r.Context(), `SELECT COALESCE(sum(kind='view'),0),COALESCE(sum(kind='download'),0),COALESCE(sum(kind='signal'),0) FROM share_events WHERE share_id=?`, r.PathValue("id")).Scan(&views, &downloads, &signals)
	if err != nil {
		fileError(w, err)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT kind,created_at FROM share_events WHERE share_id=? ORDER BY id DESC LIMIT 100`, r.PathValue("id"))
	if err != nil {
		fileError(w, err)
		return
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var kind string
		var at int64
		if err = rows.Scan(&kind, &at); err != nil {
			fileError(w, err)
			return
		}
		events = append(events, map[string]any{"kind": kind, "created_at": at})
	}
	if err = rows.Err(); err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"views": views, "download_requests": downloads, "signals": signals, "events": events})
}

// This policy selects cold local content >= 1 MiB, untouched for seven days,
// and requested at most twice. Execution is explicit and maintenance-authorized.
type StorageCandidate struct {
	FileID string `json:"file_id"`
	Size   int64  `json:"size"`
	Reason string `json:"reason"`
	key    string
}

func (a *App) candidates(ctx context.Context, owner int64) ([]StorageCandidate, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT min(f.id),b.size,b.storage_key FROM blobs b JOIN files f ON f.blob_id=b.id WHERE f.user_id=? AND f.deleted=0 AND b.backend='local' AND b.size>=1048576 AND b.access_count<=2 AND b.last_access<=? GROUP BY b.id HAVING max(f.created_at)<=? LIMIT 20`, owner, time.Now().Add(-7*24*time.Hour).Unix(), time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageCandidate{}
	for rows.Next() {
		var c StorageCandidate
		if err = rows.Scan(&c.FileID, &c.Size, &c.key); err != nil {
			return nil, err
		}
		c.Reason = "local >=1MiB; <=2 requests; untouched for 7 days"
		out = append(out, c)
	}
	return out, rows.Err()
}
func (a *App) storageCandidates(w http.ResponseWriter, r *http.Request) {
	cs, err := a.candidates(r.Context(), current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"candidates": cs, "object_storage_configured": a.store != nil})
}
func (a *App) executePolicy(w http.ResponseWriter, r *http.Request) {
	if a.cfg.MaintenanceKey == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-NetDisk-Maintenance")), []byte(a.cfg.MaintenanceKey)) != 1 {
		fail(w, 403, "maintenance authorization required")
		return
	}
	if a.store == nil {
		fail(w, 503, "object storage unavailable")
		return
	}
	cs, err := a.candidates(r.Context(), current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	done := 0
	for _, c := range cs {
		if err = a.migrateBlob(r.Context(), c.key); err != nil {
			respond(w, 503, map[string]any{"migrated": done, "error": "migration failed; recovery marker preserved"})
			return
		}
		done++
	}
	respond(w, 200, map[string]int{"migrated": done})
}
