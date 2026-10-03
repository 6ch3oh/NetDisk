package netdisk

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type Folder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parent_id"`
	CreatedAt int64  `json:"created_at"`
}
type folderProblem struct {
	status  int
	message string
}

func (e *folderProblem) Error() string { return e.message }

var missingFolder = &folderProblem{404, "folder not found"}

func folderError(w http.ResponseWriter, err error) {
	var p *folderProblem
	if errors.As(err, &p) {
		fail(w, p.status, p.message)
	} else {
		fileError(w, err)
	}
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func checkParent(ctx context.Context, q rowQuery, owner int64, id string) error {
	if id == "" {
		return nil
	}
	if !keyPattern.MatchString(id) {
		return missingFolder
	}
	var found string
	err := q.QueryRowContext(ctx, `SELECT id FROM folders WHERE id=? AND user_id=?`, id, owner).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return missingFolder
	}
	return err
}
func getFolder(ctx context.Context, q rowQuery, owner int64, id string) (Folder, error) {
	var f Folder
	if !keyPattern.MatchString(id) {
		return f, missingFolder
	}
	err := q.QueryRowContext(ctx, `SELECT id,name,COALESCE(parent_id,''),created_at FROM folders WHERE id=? AND user_id=?`, id, owner).Scan(&f.ID, &f.Name, &f.ParentID, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, missingFolder
	}
	return f, err
}

// One connection plus transactions serializes topology checks and writes.
// The migration is atomic and repeatable; legacy files acquire a NULL root.
func (a *App) migrateFolders() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS folders(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,name TEXT NOT NULL,parent_id TEXT REFERENCES folders(id) ON DELETE RESTRICT,created_at INTEGER NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS folders_sibling_name ON folders(user_id,COALESCE(parent_id,''),name);`)
	if err != nil {
		return err
	}
	rows, err := tx.Query(`PRAGMA table_info(files)`)
	if err != nil {
		return err
	}
	hasColumn := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "folder_id" {
			hasColumn = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !hasColumn {
		if _, err = tx.Exec(`ALTER TABLE files ADD COLUMN folder_id TEXT REFERENCES folders(id) ON DELETE RESTRICT`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS files_folder ON files(user_id,folder_id,deleted); PRAGMA user_version=1;`); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) folderRoutes(m *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/folders": a.allFolders, "GET /api/directory": a.directory, "GET /api/folders/{id}": a.folderMetadata,
		"POST /api/folders": a.createFolder, "PATCH /api/folders/{id}": a.renameFolder, "DELETE /api/folders/{id}": a.deleteFolder,
		"POST /api/folders/{id}/move": a.moveFolder, "POST /api/files/{id}/move": a.moveFile,
	} {
		m.Handle(pattern, a.auth(handler))
	}
}
func (a *App) allFolders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,COALESCE(parent_id,''),created_at FROM folders WHERE user_id=? ORDER BY name,id`, current(r).user.ID)
	if err != nil {
		folderError(w, err)
		return
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.CreatedAt); err != nil {
			folderError(w, err)
			return
		}
		out = append(out, f)
	}
	if err = rows.Err(); err != nil {
		folderError(w, err)
		return
	}
	respond(w, 200, map[string]any{"folders": out})
}
func (a *App) folderMetadata(w http.ResponseWriter, r *http.Request) {
	f, err := getFolder(r.Context(), a.db, current(r).user.ID, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	respond(w, 200, f)
}
func (a *App) directory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := current(r).user.ID
	id := r.URL.Query().Get("folder_id")
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	if err = checkParent(ctx, tx, owner, id); err != nil {
		folderError(w, err)
		return
	}
	crumbs := []Folder{}
	cursor := id
	seen := map[string]bool{}
	for cursor != "" {
		if seen[cursor] {
			folderError(w, errors.New("invalid folder tree"))
			return
		}
		seen[cursor] = true
		f, e := getFolder(ctx, tx, owner, cursor)
		if e != nil {
			folderError(w, e)
			return
		}
		crumbs = append(crumbs, f)
		cursor = f.ParentID
	}
	for i, j := 0, len(crumbs)-1; i < j; i, j = i+1, j-1 {
		crumbs[i], crumbs[j] = crumbs[j], crumbs[i]
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,COALESCE(parent_id,''),created_at FROM folders WHERE user_id=? AND parent_id IS ? ORDER BY name,id`, owner, nullableID(id))
	if err != nil {
		folderError(w, err)
		return
	}
	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.CreatedAt); err != nil {
			rows.Close()
			folderError(w, err)
			return
		}
		folders = append(folders, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		folderError(w, err)
		return
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,name,size,created_at,COALESCE(folder_id,'') FROM files WHERE user_id=? AND folder_id IS ? AND deleted=0 ORDER BY created_at,id`, owner, nullableID(id))
	if err != nil {
		folderError(w, err)
		return
	}
	files := []File{}
	for rows.Next() {
		var f File
		if err = rows.Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &f.FolderID); err != nil {
			rows.Close()
			folderError(w, err)
			return
		}
		link(&f)
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		folderError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		folderError(w, err)
		return
	}
	respond(w, 200, map[string]any{"folder_id": id, "breadcrumbs": crumbs, "folders": folders, "files": files})
}
func siblingAvailable(ctx context.Context, tx *sql.Tx, owner int64, parent, name, except string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM folders WHERE user_id=? AND parent_id IS ? AND name=? AND id<>?`, owner, nullableID(parent), name, except).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return &folderProblem{409, "folder name conflict"}
	}
	return nil
}
func (a *App) createFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validName(body.Name) {
		fail(w, 400, "invalid file name")
		return
	}
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	if err = checkParent(ctx, tx, owner, body.ParentID); err == nil {
		err = siblingAvailable(ctx, tx, owner, body.ParentID, body.Name, "")
	}
	if err != nil {
		folderError(w, err)
		return
	}
	id, err := randomKey(16)
	if err != nil {
		folderError(w, err)
		return
	}
	f := Folder{id, body.Name, body.ParentID, time.Now().Unix()}
	_, err = tx.ExecContext(ctx, `INSERT INTO folders(id,user_id,name,parent_id,created_at) VALUES(?,?,?,?,?)`, id, owner, f.Name, nullableID(f.ParentID), f.CreatedAt)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		folderError(w, err)
		return
	}
	respond(w, 201, f)
}
func (a *App) renameFolder(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	f, err := getFolder(ctx, tx, owner, r.PathValue("id"))
	if err == nil {
		err = siblingAvailable(ctx, tx, owner, f.ParentID, body.Name, f.ID)
	}
	if err != nil {
		folderError(w, err)
		return
	}
	_, err = tx.ExecContext(ctx, `UPDATE folders SET name=? WHERE id=? AND user_id=?`, body.Name, f.ID, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		folderError(w, err)
		return
	}
	f.Name = body.Name
	respond(w, 200, f)
}
func (a *App) deleteFolder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	f, err := getFolder(ctx, tx, owner, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM folders WHERE parent_id=?)+(SELECT count(*) FROM files WHERE folder_id=?)+(SELECT count(*) FROM upload_sessions WHERE folder_id=?)`, f.ID, f.ID, f.ID).Scan(&count)
	if err != nil {
		folderError(w, err)
		return
	}
	if count > 0 {
		folderError(w, &folderProblem{409, "folder not empty"})
		return
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM folders WHERE id=? AND user_id=?`, f.ID, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		folderError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *App) moveFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentID string `json:"parent_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	f, err := getFolder(ctx, tx, owner, r.PathValue("id"))
	if err == nil {
		err = checkParent(ctx, tx, owner, body.ParentID)
	}
	if err != nil {
		folderError(w, err)
		return
	}
	var cycle int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (SELECT id,parent_id FROM folders WHERE id=? AND user_id=? UNION SELECT f.id,f.parent_id FROM folders f JOIN ancestors a ON f.id=a.parent_id WHERE f.user_id=?) SELECT count(*) FROM ancestors WHERE id=?`, body.ParentID, owner, owner, f.ID).Scan(&cycle)
	if err != nil {
		folderError(w, err)
		return
	}
	if cycle > 0 {
		folderError(w, &folderProblem{409, "folder cycle"})
		return
	}
	if err = siblingAvailable(ctx, tx, owner, body.ParentID, f.Name, f.ID); err != nil {
		folderError(w, err)
		return
	}
	_, err = tx.ExecContext(ctx, `UPDATE folders SET parent_id=? WHERE id=? AND user_id=?`, nullableID(body.ParentID), f.ID, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		folderError(w, err)
		return
	}
	f.ParentID = body.ParentID
	respond(w, 200, f)
}
func (a *App) moveFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FolderID string `json:"folder_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	owner := current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM files WHERE id=? AND user_id=? AND deleted=0`, r.PathValue("id"), owner).Scan(&id)
	if err == nil {
		err = checkParent(ctx, tx, owner, body.FolderID)
	}
	if err != nil {
		folderError(w, err)
		return
	}
	_, err = tx.ExecContext(ctx, `UPDATE files SET folder_id=? WHERE id=? AND user_id=? AND deleted=0`, nullableID(body.FolderID), id, owner)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		folderError(w, err)
		return
	}
	w.WriteHeader(204)
}
