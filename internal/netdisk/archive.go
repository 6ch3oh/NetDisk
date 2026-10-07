package netdisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Backup metadata contains no storage keys, credentials, or sharing capabilities.
type BackupFile struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	FolderID  string   `json:"folder_id"`
	Size      int64    `json:"size"`
	CreatedAt int64    `json:"created_at"`
	SHA256    string   `json:"sha256"`
	Folders   []Folder `json:"folders"`
}
type DiskArchive struct {
	ID             string     `json:"id"`
	File           BackupFile `json:"file"`
	DiskID         string     `json:"disk_id"`
	DiskLabel      string     `json:"disk_label"`
	LocalPath      string     `json:"local_path"`
	ArchivedAt     int64      `json:"archived_at"`
	RestoredFileID string     `json:"restored_file_id"`
	CleanupPending bool       `json:"cleanup_pending,omitempty"`
}

func (a *App) migrateArchives() error {
	_, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS disk_archives(
 id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 manifest TEXT NOT NULL,disk_id TEXT NOT NULL,disk_label TEXT NOT NULL,local_path TEXT NOT NULL,
 archived_at INTEGER NOT NULL,restored_file_id TEXT NOT NULL DEFAULT '');
 CREATE INDEX IF NOT EXISTS disk_archives_owner ON disk_archives(user_id,archived_at);`)
	return err
}
func (a *App) archiveRoutes(m *http.ServeMux) {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/backup/manifest":        a.backupManifest,
		"GET /api/archives":               a.listArchives,
		"POST /api/archives":              a.archiveToDisk,
		"POST /api/archives/{id}/restore": a.markArchiveRestored,
	} {
		m.Handle(pattern, a.auth(h))
	}
}

func backupFile(ctx context.Context, tx *sql.Tx, owner int64, id string) (BackupFile, error) {
	f := BackupFile{Folders: []Folder{}}
	err := tx.QueryRowContext(ctx, `SELECT f.id,f.name,COALESCE(f.folder_id,''),f.size,f.created_at,b.content_hash FROM files f JOIN blobs b ON b.id=f.blob_id WHERE f.id=? AND f.user_id=? AND f.deleted=0`, id, owner).Scan(&f.ID, &f.Name, &f.FolderID, &f.Size, &f.CreatedAt, &f.SHA256)
	if err != nil {
		return f, err
	}
	seen := map[string]bool{}
	for id := f.FolderID; id != ""; {
		if seen[id] || len(seen) >= 256 {
			return f, errors.New("invalid or excessively deep folder tree")
		}
		seen[id] = true
		folder, err := getFolder(ctx, tx, owner, id)
		if err != nil {
			return f, err
		}
		f.Folders = append(f.Folders, folder)
		id = folder.ParentID
	}
	for i, j := 0, len(f.Folders)-1; i < j; i, j = i+1, j-1 {
		f.Folders[i], f.Folders[j] = f.Folders[j], f.Folders[i]
	}
	return f, nil
}
func (a *App) backupManifest(w http.ResponseWriter, r *http.Request) {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM files WHERE user_id=? AND deleted=0 ORDER BY id`, current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	files := []BackupFile{}
	for _, id := range ids {
		if err != nil {
			break
		}
		var f BackupFile
		f, err = backupFile(r.Context(), tx, current(r).user.ID, id)
		files = append(files, f)
	}
	if err != nil {
		fileError(w, err)
		return
	}
	// Empty directories are included so restoration does not lose folder topology.
	rows, err = tx.QueryContext(r.Context(), `SELECT id,name,COALESCE(parent_id,''),created_at FROM folders WHERE user_id=? ORDER BY id`, current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.CreatedAt); err != nil {
			break
		}
		folders = append(folders, f)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"version": 1, "files": files, "folders": folders})
}

type archiveRow interface{ Scan(...any) error }

func scanArchive(row archiveRow) (DiskArchive, error) {
	var v DiskArchive
	var data string
	err := row.Scan(&v.ID, &data, &v.DiskID, &v.DiskLabel, &v.LocalPath, &v.ArchivedAt, &v.RestoredFileID)
	if err == nil {
		err = json.Unmarshal([]byte(data), &v.File)
	}
	return v, err
}

const archiveColumns = `id,manifest,disk_id,disk_label,local_path,archived_at,restored_file_id`

func (a *App) listArchives(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT `+archiveColumns+` FROM disk_archives WHERE user_id=? ORDER BY archived_at DESC,id`, current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	defer rows.Close()
	items := []DiskArchive{}
	for rows.Next() {
		v, e := scanArchive(rows)
		if e != nil {
			fileError(w, e)
			return
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"archives": items})
}

var backupHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (a *App) archiveToDisk(w http.ResponseWriter, r *http.Request) {
	var body DiskArchive
	if !decode(w, r, &body) {
		return
	}
	if !keyPattern.MatchString(body.ID) || !keyPattern.MatchString(body.DiskID) || !keyPattern.MatchString(body.File.ID) || !backupHashPattern.MatchString(body.File.SHA256) || body.File.Size < 0 || !validName(body.DiskLabel) || len(body.DiskLabel) > 160 || !fs.ValidPath(body.LocalPath) || strings.ContainsAny(body.LocalPath, `\:`) || len(body.LocalPath) > 512 {
		fail(w, 400, "invalid archive receipt")
		return
	}
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	previous, err := scanArchive(tx.QueryRowContext(ctx, `SELECT `+archiveColumns+` FROM disk_archives WHERE id=? AND user_id=?`, body.ID, owner))
	if err == nil {
		if !reflect.DeepEqual(previous.File, body.File) || previous.DiskID != body.DiskID || previous.LocalPath != body.LocalPath {
			fail(w, 409, "archive receipt conflict")
			return
		}
		respond(w, 200, previous)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		fileError(w, err)
		return
	}
	currentFile, err := backupFile(ctx, tx, owner, body.File.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	// Compare content AND complete logical path within the same transaction as
	// releasing the reference. NFS writes/renames/moves cannot slip between them.
	if !reflect.DeepEqual(currentFile, body.File) {
		fail(w, 409, "file changed since backup; back up again")
		return
	}
	encoded, _ := json.Marshal(currentFile)
	body.ArchivedAt = time.Now().Unix()
	body.RestoredFileID = ""
	body.CleanupPending = false
	_, err = tx.ExecContext(ctx, `INSERT INTO disk_archives(id,user_id,manifest,disk_id,disk_label,local_path,archived_at) VALUES(?,?,?,?,?,?,?)`, body.ID, owner, string(encoded), body.DiskID, body.DiskLabel, body.LocalPath, body.ArchivedAt)
	if err == nil {
		err = releaseFile(tx, body.File.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	// ref_count=0 is durable cleanup work. Shared content stays until its last
	// reference is released; revocation of file shares is part of releaseFile.
	body.CleanupPending = a.gcBlobs(ctx) != nil
	respond(w, 201, body)
}
func (a *App) markArchiveRestored(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FileID string `json:"file_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !keyPattern.MatchString(r.PathValue("id")) || !keyPattern.MatchString(body.FileID) {
		fail(w, 400, "invalid restoration")
		return
	}
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		fileError(w, err)
		return
	}
	defer tx.Rollback()
	v, err := scanArchive(tx.QueryRowContext(ctx, `SELECT `+archiveColumns+` FROM disk_archives WHERE id=? AND user_id=?`, r.PathValue("id"), owner))
	if err != nil {
		fileError(w, err)
		return
	}
	f, err := backupFile(ctx, tx, owner, body.FileID)
	if err != nil {
		fileError(w, err)
		return
	}
	if f.SHA256 != v.File.SHA256 || f.Size != v.File.Size || f.Name != v.File.Name {
		fail(w, 409, "restored content does not match archive")
		return
	}
	_, err = tx.ExecContext(ctx, `UPDATE disk_archives SET restored_file_id=? WHERE id=? AND user_id=?`, f.ID, v.ID, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	v.RestoredFileID = f.ID
	respond(w, 200, v)
}
