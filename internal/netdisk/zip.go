package netdisk

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type zipFile struct {
	File
	Key  string
	Hash string
}
type zipSnapshot struct {
	Owner   int64
	Root    string
	Folders []Folder
	Files   []zipFile
}

func (a *App) snapshot(ctx context.Context, owner int64, root string) (zipSnapshot, error) {
	s := zipSnapshot{Owner: owner, Root: root}
	if _, err := getFolder(ctx, a.db, owner, root); err != nil {
		return s, err
	}
	rows, err := a.db.QueryContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT id FROM folders WHERE id=? AND user_id=? UNION SELECT f.id FROM folders f JOIN tree t ON f.parent_id=t.id WHERE f.user_id=?) SELECT f.id,f.name,COALESCE(f.parent_id,''),f.created_at FROM folders f JOIN tree t ON f.id=t.id ORDER BY f.id`, root, owner, owner)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.CreatedAt); err != nil {
			rows.Close()
			return s, err
		}
		s.Folders = append(s.Folders, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	rows, err = a.db.QueryContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT id FROM folders WHERE id=? AND user_id=? UNION SELECT f.id FROM folders f JOIN tree t ON f.parent_id=t.id WHERE f.user_id=?) SELECT f.id,f.name,f.size,f.created_at,f.folder_id,b.storage_key,b.content_hash FROM files f JOIN blobs b ON f.blob_id=b.id JOIN tree t ON f.folder_id=t.id WHERE f.user_id=? AND f.deleted=0 ORDER BY f.id`, root, owner, owner, owner)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var f zipFile
		if err = rows.Scan(&f.ID, &f.Name, &f.Size, &f.CreatedAt, &f.FolderID, &f.Key, &f.Hash); err != nil {
			return s, err
		}
		s.Files = append(s.Files, f)
	}
	return s, rows.Err()
}
func safeZIPSegment(name string) bool { return validName(name) && !strings.Contains(name, ":") }
func safeZIPPath(name string) bool {
	cleaned := strings.TrimSuffix(name, "/")
	if cleaned == "" || strings.HasPrefix(cleaned, "/") || strings.ContainsAny(cleaned, "\\:") || path.Clean(cleaned) != cleaned {
		return false
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if !safeZIPSegment(segment) {
			return false
		}
	}
	return true
}
func uniqueZIPName(used map[string]bool, parent, name, id string) string {
	candidate := path.Join(parent, name)
	for i := 0; used[candidate]; i++ {
		candidate = path.Join(parent, fmt.Sprintf("%s [%s-%d]", name, id, i))
	}
	used[candidate] = true
	return candidate
}
func (a *App) generateZIP(ctx context.Context, s zipSnapshot, target io.Writer) error {
	z := zip.NewWriter(target)
	defer z.Close()
	folders := map[string]Folder{}
	for _, f := range s.Folders {
		if !safeZIPSegment(f.Name) {
			return errors.New("unsafe ZIP folder name")
		}
		folders[f.ID] = f
	}
	paths := map[string]string{s.Root: ""}
	used := map[string]bool{}
	visiting := map[string]bool{}
	var folderPath func(string) (string, error)
	folderPath = func(id string) (string, error) {
		if p, ok := paths[id]; ok {
			return p, nil
		}
		if visiting[id] {
			return "", errors.New("invalid folder tree")
		}
		visiting[id] = true
		f, ok := folders[id]
		if !ok {
			return "", errors.New("folder outside snapshot")
		}
		parent, err := folderPath(f.ParentID)
		if err != nil {
			return "", err
		}
		p := uniqueZIPName(used, parent, f.Name, f.ID)
		if !safeZIPPath(p) {
			return "", errors.New("unsafe ZIP entry")
		}
		paths[id] = p
		delete(visiting, id)
		return p, nil
	}
	// Reserve directories first; same-name files get an ID suffix in stable ID order.
	for _, f := range s.Folders {
		if f.ID == s.Root {
			continue
		}
		p, err := folderPath(f.ID)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: p + "/", Method: zip.Store}
		header.SetModTime(time.Unix(f.CreatedAt, 0))
		header.SetMode(0700 | os.ModeDir)
		if _, err = z.CreateHeader(header); err != nil {
			return err
		}
	}
	for _, f := range s.Files {
		if !safeZIPSegment(f.Name) || !keyPattern.MatchString(f.Key) {
			return errors.New("unsafe ZIP file name or key")
		}
		parent, err := folderPath(f.FolderID)
		if err != nil {
			return err
		}
		p := uniqueZIPName(used, parent, f.Name, f.ID)
		if !safeZIPPath(p) {
			return errors.New("unsafe ZIP entry")
		}
		header := &zip.FileHeader{Name: p, Method: zip.Deflate}
		header.SetModTime(time.Unix(f.CreatedAt, 0))
		header.SetMode(0600)
		writer, err := z.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := a.openBlob(ctx, f.Key)
		if err != nil {
			return err
		}
		size, e := io.CopyBuffer(writer, io.LimitReader(source, f.Size+1), make([]byte, 32*1024))
		closeErr := source.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if size != f.Size {
			return errors.New("ZIP source size mismatch")
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return z.Close()
}
func (a *App) folderZIP(w http.ResponseWriter, r *http.Request) {
	owner := current(r).user.ID
	root := r.PathValue("id")
	ctx := r.Context()
	folder, err := getFolder(ctx, a.db, owner, root)
	if err != nil {
		folderError(w, err)
		return
	}
	snapshot, err := a.snapshot(ctx, owner, root)
	if err != nil {
		folderError(w, err)
		return
	}
	cacheKey := fingerprint(snapshot)
	var key string
	var expires int64
	err = a.db.QueryRowContext(ctx, `SELECT storage_key,expires_at FROM zip_cache WHERE cache_key=? AND user_id=?`, cacheKey, owner).Scan(&key, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fileError(w, err)
		return
	}
	if err == nil && expires <= time.Now().Unix() {
		if err = a.cleanupLocked(time.Now()); err != nil {
			fileError(w, err)
			return
		}
		key = ""
	}
	if key != "" {
		if _, e := os.Stat(filepath.Join(a.temp, key)); errors.Is(e, os.ErrNotExist) {
			_, err = a.db.ExecContext(ctx, `DELETE FROM zip_cache WHERE cache_key=?`, cacheKey)
			key = ""
		} else if e != nil {
			fileError(w, e)
			return
		}
	}
	if key == "" {
		tmp, e := os.CreateTemp(a.temp, "upload-")
		if e != nil {
			fileError(w, e)
			return
		}
		defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
		if err = a.generateZIP(ctx, snapshot, tmp); err != nil {
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
		key, err = randomKey(16)
		if err != nil {
			fileError(w, err)
			return
		}
		if err = os.Rename(tmp.Name(), filepath.Join(a.temp, key)); err != nil {
			fileError(w, err)
			return
		}
		_, err = a.db.ExecContext(ctx, `INSERT INTO zip_cache(cache_key,storage_key,user_id,folder_id,expires_at) VALUES(?,?,?,?,?)`, cacheKey, key, owner, root, time.Now().Add(15*time.Minute).Unix())
		if err != nil {
			os.Remove(filepath.Join(a.temp, key))
			fileError(w, err)
			return
		}
	}
	source, err := os.Open(filepath.Join(a.temp, key))
	if err != nil {
		fileError(w, err)
		return
	}
	defer source.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": folder.Name + ".zip"}))
	http.ServeContent(w, r, folder.Name+".zip", time.Time{}, source)
}
