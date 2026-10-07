package netdisk

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
)

// Selection downloads recheck every resource's owner on HEAD, GET and Range.
// Reuse the streaming ZIP writer, snapshot cache, invalidation and durable GC.
func selectionIDs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		if !keyPattern.MatchString(id) {
			return nil, &folderProblem{400, "invalid selection"}
		}
		seen[id] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func (a *App) selectionZIP(w http.ResponseWriter, r *http.Request) {
	files, err := selectionIDs(r.URL.Query().Get("file_ids"))
	if err != nil {
		folderError(w, err)
		return
	}
	folders, err := selectionIDs(r.URL.Query().Get("folder_ids"))
	if err != nil {
		folderError(w, err)
		return
	}
	if len(files)+len(folders) == 0 || len(files)+len(folders) > 200 {
		fail(w, 400, "select 1..200 resources")
		return
	}
	snapshot, err := a.selectionSnapshot(r.Context(), current(r).user.ID, files, folders)
	if err != nil {
		folderError(w, err)
		return
	}
	a.serveZIP(w, r, snapshot, "网盘文件.zip")
}
func (a *App) selectionSnapshot(ctx context.Context, owner int64, fileIDs, folderIDs []string) (zipSnapshot, error) {
	s := zipSnapshot{Owner: owner}
	folders, files := map[string]Folder{}, map[string]zipFile{}
	for _, id := range folderIDs {
		tree, err := a.snapshot(ctx, owner, id)
		if err != nil {
			return s, err
		}
		for _, folder := range tree.Folders {
			folders[folder.ID] = folder
		}
		for _, file := range tree.Files {
			files[file.ID] = file
		}
	}
	for _, id := range fileIDs {
		var file zipFile
		err := a.db.QueryRowContext(ctx, `SELECT f.id,f.name,f.size,f.created_at,COALESCE(f.folder_id,''),b.storage_key,b.content_hash FROM files f JOIN blobs b ON f.blob_id=b.id WHERE f.id=? AND f.user_id=? AND f.deleted=0`, id, owner).Scan(&file.ID, &file.Name, &file.Size, &file.CreatedAt, &file.FolderID, &file.Key, &file.Hash)
		if err != nil {
			return s, err
		}
		if _, included := files[id]; !included {
			file.FolderID = ""
			files[id] = file
		}
	}
	for _, folder := range folders {
		if _, included := folders[folder.ParentID]; !included {
			folder.ParentID = ""
		}
		s.Folders = append(s.Folders, folder)
	}
	for _, file := range files {
		s.Files = append(s.Files, file)
	}
	sort.Slice(s.Folders, func(i, j int) bool { return s.Folders[i].ID < s.Folders[j].ID })
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].ID < s.Files[j].ID })
	return s, nil
}

// Recursive deletion is explicit and leaves the existing empty-folder API intact.
// Metadata/refcounts/part cleanup markers commit together, before physical GC.
func (a *App) deleteFolderTree(w http.ResponseWriter, r *http.Request) {
	ctx, owner := r.Context(), current(r).user.ID
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		folderError(w, err)
		return
	}
	defer tx.Rollback()
	root, err := getFolder(ctx, tx, owner, r.PathValue("id"))
	if err != nil {
		folderError(w, err)
		return
	}
	const tree = `WITH RECURSIVE tree(id) AS (SELECT id FROM folders WHERE id=? AND user_id=? UNION SELECT f.id FROM folders f JOIN tree t ON f.parent_id=t.id WHERE f.user_id=?) `
	folderIDs, err := selectionQueryIDs(ctx, tx, tree+`SELECT id FROM tree`, root.ID, owner, owner)
	if err != nil {
		folderError(w, err)
		return
	}
	fileIDs, err := selectionQueryIDs(ctx, tx, tree+`SELECT f.id FROM files f JOIN tree t ON f.folder_id=t.id WHERE f.user_id=?`, root.ID, owner, owner, owner)
	if err != nil {
		folderError(w, err)
		return
	}
	for _, id := range fileIDs {
		if err = releaseFile(tx, id); err != nil {
			folderError(w, err)
			return
		}
	}
	for _, id := range folderIDs {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO temp_cleanup SELECT p.storage_key FROM upload_parts p JOIN upload_sessions s ON p.session_id=s.id WHERE s.folder_id=? AND s.user_id=?`, id, owner)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM upload_requests WHERE user_id=? AND session_id IN (SELECT id FROM upload_sessions WHERE folder_id=? AND user_id=?)`, owner, id, owner)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE folder_id=? AND user_id=?`, id, owner)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE folders SET parent_id=NULL WHERE id=? AND user_id=?`, id, owner)
		}
		if err != nil {
			folderError(w, err)
			return
		}
	}
	for _, id := range folderIDs {
		if _, err = tx.ExecContext(ctx, `DELETE FROM folders WHERE id=? AND user_id=?`, id, owner); err != nil {
			folderError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		folderError(w, err)
		return
	}
	if err = a.cleanupKeys(ctx, "temp_cleanup", a.temp); err == nil {
		err = a.gcBlobs(ctx)
	}
	if err != nil {
		fail(w, 503, "deletion pending storage recovery")
		return
	}
	w.WriteHeader(204)
}
func selectionQueryIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
