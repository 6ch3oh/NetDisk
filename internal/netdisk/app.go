package netdisk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const cookieName = "netdisk_session"
const sessionTTL = 24 * time.Hour

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
var keyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Config struct {
	DataDir        string
	Origin         string
	MaxUploadBytes int64
	CookieSecure   bool
	BcryptCost     int // Zero selects the production default (12).
}
type App struct {
	db        *sql.DB
	cfg       Config
	blobs     string
	temp      string
	dummyHash []byte
}
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	CreatedAt   int64  `json:"created_at"`
	DownloadURL string `json:"download_url"`
	FolderID    string `json:"folder_id,omitempty"`
}
type identity struct {
	user        User
	sessionHash string
}
type identityKey struct{}

func New(cfg Config) (*App, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("origin must be an absolute HTTP(S) origin without a trailing slash")
	}
	if cfg.CookieSecure && u.Scheme != "https" {
		return nil, errors.New("Secure cookie requires an HTTPS origin")
	}
	if !cfg.CookieSecure && u.Scheme != "http" {
		return nil, errors.New("HTTPS origin requires Secure cookie")
	}
	if !cfg.CookieSecure && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, errors.New("insecure HTTP origin must be loopback")
	}
	if cfg.MaxUploadBytes <= 0 {
		return nil, errors.New("upload limit must be positive")
	}
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = 12
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	cfg.DataDir = abs
	a := &App{cfg: cfg, blobs: filepath.Join(abs, "blobs"), temp: filepath.Join(abs, "tmp")}
	for _, dir := range []string{abs, a.blobs, a.temp} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	dbURL := &url.URL{Scheme: "file", Path: filepath.Join(abs, "netdisk.db")}
	dbURL.RawQuery = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	a.db, err = sql.Open("sqlite", dbURL.String())
	if err != nil {
		return nil, err
	}
	a.db.SetMaxOpenConns(1)
	_, err = a.db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS users (
 id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS files (
 id TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, storage_key TEXT NOT NULL UNIQUE, size INTEGER NOT NULL CHECK(size>=0),
 created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS files_owner ON files(user_id,deleted);`)
	if err != nil {
		a.db.Close()
		return nil, err
	}
	if err = a.migrateFolders(); err != nil {
		a.db.Close()
		return nil, err
	}
	a.dummyHash, err = bcrypt.GenerateFromPassword([]byte("invalid-account-placeholder"), cfg.BcryptCost)
	if err == nil {
		err = a.recoverDeletes()
	}
	if err != nil {
		a.db.Close()
		return nil, err
	}
	return a, nil
}
func (a *App) Close() error { return a.db.Close() }
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	a.webRoutes(m)
	a.folderRoutes(m)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			fail(w, 503, "unavailable")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("POST /api/register", a.register)
	m.HandleFunc("POST /api/login", a.login)
	m.Handle("POST /api/logout", a.auth(http.HandlerFunc(a.logout)))
	m.Handle("GET /api/me", a.auth(http.HandlerFunc(a.me)))
	m.Handle("GET /api/files", a.auth(http.HandlerFunc(a.list)))
	m.Handle("POST /api/files", a.auth(http.HandlerFunc(a.upload)))
	m.Handle("GET /api/files/{id}", a.auth(http.HandlerFunc(a.metadata)))
	m.Handle("GET /api/files/{id}/download", a.auth(http.HandlerFunc(a.download)))
	m.Handle("PATCH /api/files/{id}", a.auth(http.HandlerFunc(a.rename)))
	m.Handle("DELETE /api/files/{id}", a.auth(http.HandlerFunc(a.delete)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		// A mandatory non-simple header forces cross-origin browsers to preflight.
		// No CORS permission is granted; additionally validate Origin and Fetch Metadata.
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("X-NetDisk-Request") != "1" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.cfg.Origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, "request verification failed")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "expected application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		fail(w, 400, "invalid JSON body")
		return false
	}
	if err = d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "expected one JSON value")
		return false
	}
	return true
}
func randomKey(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func tokenHash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}
func current(r *http.Request) identity { return r.Context().Value(identityKey{}).(identity) }
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			fail(w, 401, "authentication required")
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil || len(raw) != 32 {
			fail(w, 401, "authentication required")
			return
		}
		i := identity{sessionHash: tokenHash(c.Value)}
		err = a.db.QueryRowContext(r.Context(), `SELECT u.id,u.username FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, i.sessionHash, time.Now().Unix()).Scan(&i.user.ID, &i.user.Username)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 401, "authentication required")
			return
		}
		if err != nil {
			fail(w, 500, "storage unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, i)))
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	c.Username = strings.ToLower(strings.TrimSpace(c.Username))
	if !usernamePattern.MatchString(c.Username) || len(c.Password) < 10 || len(c.Password) > 72 {
		fail(w, 400, "username must be 3-32 lowercase letters, digits or underscores; password must be 10-72 bytes")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), a.cfg.BcryptCost)
	if err != nil {
		fail(w, 500, "password hashing failed")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `INSERT INTO users(username,password_hash,created_at) VALUES(?,?,?) ON CONFLICT(username) DO NOTHING`, c.Username, string(hash), time.Now().Unix())
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		fail(w, 409, "username unavailable")
		return
	}
	id, _ := res.LastInsertId()
	respond(w, 201, User{id, c.Username})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	c.Username = strings.ToLower(strings.TrimSpace(c.Username))
	var u User
	var hash string
	err := a.db.QueryRowContext(r.Context(), `SELECT id,username,password_hash FROM users WHERE username=?`, c.Username).Scan(&u.ID, &u.Username, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "storage unavailable")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(a.dummyHash, []byte(c.Password))
		fail(w, 401, "invalid credentials")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		fail(w, 401, "invalid credentials")
		return
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		fail(w, 500, "session creation failed")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expires := time.Now().Add(sessionTTL)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE expires_at<=?`, time.Now().Unix())
	if err == nil {
		if old, e := r.Cookie(cookieName); e == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=?`, tokenHash(old.Value))
		}
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)`, tokenHash(token), u.ID, expires.Unix())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure, MaxAge: int(sessionTTL.Seconds()), Expires: expires})
	respond(w, 200, u)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=?`, current(r).sessionHash); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure, MaxAge: -1, Expires: time.Unix(1, 0)})
	w.WriteHeader(204)
}
func (a *App) me(w http.ResponseWriter, r *http.Request) { respond(w, 200, current(r).user) }
func validName(s string) bool {
	if s == "" || s == "." || s == ".." || s != strings.TrimSpace(s) || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 255 || strings.ContainsAny(s, "/\\") {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func link(f *File) { f.DownloadURL = "/api/files/" + f.ID + "/download" }
func (a *App) owned(r *http.Request) (File, string, error) {
	var f File
	var key string
	if !keyPattern.MatchString(r.PathValue("id")) {
		return f, key, sql.ErrNoRows
	}
	err := a.db.QueryRowContext(r.Context(), `SELECT id,name,size,created_at,storage_key,COALESCE(folder_id,'') FROM files WHERE id=? AND user_id=? AND deleted=0`, r.PathValue("id"), current(r).user.ID).Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &key, &f.FolderID)
	link(&f)
	if err == nil && !keyPattern.MatchString(key) {
		err = errors.New("invalid stored key")
	}
	return f, key, err
}
func fileError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "file not found")
	} else {
		fail(w, 500, "storage unavailable")
	}
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,size,created_at,COALESCE(folder_id,'') FROM files WHERE user_id=? AND deleted=0 ORDER BY created_at,id`, current(r).user.ID)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer rows.Close()
	files := []File{}
	for rows.Next() {
		var f File
		if err = rows.Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &f.FolderID); err != nil {
			fail(w, 500, "storage unavailable")
			return
		}
		link(&f)
		files = append(files, f)
	}
	if rows.Err() != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	respond(w, 200, map[string]any{"files": files})
}
func (a *App) metadata(w http.ResponseWriter, r *http.Request) {
	f, _, err := a.owned(r)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, f)
}
func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	folderID := r.URL.Query().Get("folder_id")
	if err := checkParent(r.Context(), a.db, current(r).user.ID, folderID); err != nil {
		folderError(w, err)
		return
	}
	name := r.URL.Query().Get("name")
	if !validName(name) {
		fail(w, 400, "invalid file name")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/octet-stream" {
		fail(w, 415, "expected application/octet-stream")
		return
	}
	if r.ContentLength > a.cfg.MaxUploadBytes {
		fail(w, 413, "upload too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	tmp, err := os.CreateTemp(a.temp, "upload-")
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	// The body is never ReadAll'd or parsed into a multipart form. Memory is bounded
	// by this 32 KiB buffer, independent of the upload's total size.
	size, err := io.CopyBuffer(tmp, r.Body, make([]byte, 32*1024))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fail(w, 413, "upload too large")
		} else {
			fail(w, 400, "incomplete upload")
		}
		return
	}
	if err = tmp.Sync(); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	if err = tmp.Close(); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	id, err := randomKey(16)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	key, err := randomKey(16)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	path := filepath.Join(a.blobs, key)
	if err = os.Rename(tmp.Name(), path); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	f := File{ID: id, Name: name, Size: size, CreatedAt: time.Now().Unix(), FolderID: folderID}
	link(&f)
	err = a.insertFile(r.Context(), current(r).user.ID, f, key)
	if err != nil {
		os.Remove(path)
		folderError(w, err)
		return
	}
	respond(w, 201, f)
}
func (a *App) download(w http.ResponseWriter, r *http.Request) {
	f, key, err := a.owned(r)
	if err != nil {
		fileError(w, err)
		return
	}
	blob, err := os.Open(filepath.Join(a.blobs, key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, 404, "file not found")
		} else {
			fail(w, 500, "storage unavailable")
		}
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	http.ServeContent(w, r, f.Name, time.Unix(f.CreatedAt, 0), blob)
}
func (a *App) rename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validName(body.Name) {
		fail(w, 400, "invalid file name")
		return
	}
	f, _, err := a.owned(r)
	if err != nil {
		fileError(w, err)
		return
	}
	res, err := a.db.ExecContext(r.Context(), `UPDATE files SET name=? WHERE id=? AND user_id=? AND deleted=0`, body.Name, f.ID, current(r).user.ID)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "file not found")
		return
	}
	f.Name = body.Name
	respond(w, 200, f)
}
func (a *App) delete(w http.ResponseWriter, r *http.Request) {
	f, key, err := a.owned(r)
	if err != nil {
		fileError(w, err)
		return
	}
	// Hide the record durably before unlinking; a crash never resurrects a
	// download. Startup retries interrupted deletions using the tombstone.
	res, err := a.db.ExecContext(r.Context(), `UPDATE files SET deleted=1 WHERE id=? AND user_id=? AND deleted=0`, f.ID, current(r).user.ID)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "file not found")
		return
	}
	if err = a.finishDelete(f.ID, key); err != nil {
		fail(w, 500, "deletion pending storage recovery")
		return
	}
	w.WriteHeader(204)
}
func (a *App) finishDelete(id, key string) error {
	if !keyPattern.MatchString(key) {
		return errors.New("invalid stored key")
	}
	if err := os.Remove(filepath.Join(a.blobs, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := a.db.Exec(`DELETE FROM files WHERE id=? AND deleted=1`, id)
	return err
}
func (a *App) recoverDeletes() error {
	rows, err := a.db.Query(`SELECT id,storage_key FROM files WHERE deleted=1`)
	if err != nil {
		return err
	}
	type pending struct{ id, key string }
	var items []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.key); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		if err = a.finishDelete(p.id, p.key); err != nil {
			return fmt.Errorf("recover pending deletion: %w", err)
		}
	}
	return nil
}
