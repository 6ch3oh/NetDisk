package netdisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
)

func (a *App) nfsRoutes(m *http.ServeMux) {
	m.Handle("POST /api/nfs/exports", a.auth(http.HandlerFunc(a.createExport)))
	m.Handle("GET /api/nfs/exports", a.auth(http.HandlerFunc(a.listExports)))
	m.Handle("DELETE /api/nfs/exports/{id}", a.auth(http.HandlerFunc(a.revokeExport)))
}
func (a *App) createExport(w http.ResponseWriter, r *http.Request) {
	id, err := randomKey(16)
	if err != nil {
		fileError(w, err)
		return
	}
	token, err := randomKey(32)
	if err != nil {
		fileError(w, err)
		return
	}
	expires := time.Now().Add(24 * time.Hour).Unix()
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO nfs_exports(id,user_id,token_hash,expires_at) VALUES(?,?,?,?)`, id, current(r).user.ID, tokenHash(token), expires)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 201, map[string]any{"id": id, "mount_path": "/" + token, "expires_at": expires})
}
func (a *App) listExports(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,expires_at FROM nfs_exports WHERE user_id=? AND expires_at>?`, current(r).user.ID, time.Now().Unix())
	if err != nil {
		fileError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id string
		var expires int64
		if err = rows.Scan(&id, &expires); err != nil {
			fileError(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "expires_at": expires})
	}
	if err = rows.Err(); err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]any{"exports": out})
}
func (a *App) revokeExport(w http.ResponseWriter, r *http.Request) {
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM nfs_exports WHERE id=? AND user_id=?`, r.PathValue("id"), current(r).user.ID)
	if err != nil {
		fileError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fileError(w, sql.ErrNoRows)
		return
	}
	w.WriteHeader(204)
}

// NFSv3 is a LAN protocol without transport encryption. The application binds
// this optional listener to loopback; Linux clients may use an SSH tunnel.
// Handles identify logical paths and recheck revocable export ownership.
type nfsRef struct {
	fs    *logicalFS
	parts []string
}
type logicalNFS struct {
	a       *App
	mu      sync.Mutex
	handles map[string]nfsRef
	reverse map[string]string
}

func (a *App) NFSHandler() nfs.Handler {
	return &logicalNFS{a: a, handles: map[string]nfsRef{}, reverse: map[string]string{}}
}
func (a *App) ServeNFS(l net.Listener) error { return nfs.Serve(privateNFSListener{l}, a.NFSHandler()) }

type privateNFSListener struct{ net.Listener }

func (l privateNFSListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(c.RemoteAddr().String())
		ip := net.ParseIP(host)
		if err == nil && ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
			return c, nil
		}
		c.Close()
	}
}
func (h *logicalNFS) Mount(ctx context.Context, c net.Conn, r nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	token := strings.TrimPrefix(string(r.Dirpath), "/")
	if len(token) != 64 || strings.Contains(token, "/") {
		return nfs.MountStatus(13), nil, nil
	}
	f := &logicalFS{a: h.a}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if err := h.a.db.QueryRowContext(ctx, `SELECT id,user_id FROM nfs_exports WHERE token_hash=? AND expires_at>?`, tokenHash(token), time.Now().Unix()).Scan(&f.export, &f.owner); err != nil {
		return nfs.MountStatus(13), nil, nil
	}
	return nfs.MountStatusOk, f, []nfs.AuthFlavor{nfs.AuthFlavorNull, nfs.AuthFlavorUnix}
}
func (h *logicalNFS) Change(fs billy.Filesystem) billy.Change { f, _ := fs.(billy.Change); return f }
func (h *logicalNFS) FSStat(ctx context.Context, fs billy.Filesystem, s *nfs.FSStat) error {
	f, ok := fs.(*logicalFS)
	if !ok {
		return os.ErrPermission
	}
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	if err := f.check(); err != nil {
		return err
	}
	var used, reserved int64
	err := f.a.db.QueryRowContext(ctx, `SELECT (SELECT COALESCE(sum(size),0) FROM files WHERE user_id=? AND deleted=0),(SELECT COALESCE(sum(expected_size),0) FROM upload_sessions WHERE user_id=? AND expires_at>?)`, f.owner, f.owner, time.Now().Unix()).Scan(&used, &reserved)
	if err != nil {
		return err
	}
	free := max(0, f.a.cfg.QuotaBytes-used-reserved)
	s.TotalSize = uint64(f.a.cfg.QuotaBytes)
	s.FreeSize = uint64(free)
	s.AvailableSize = uint64(free)
	return nil
}
func (h *logicalNFS) HandleLimit() int { return 4096 }
func (h *logicalNFS) ToHandle(fs billy.Filesystem, parts []string) []byte {
	f, ok := fs.(*logicalFS)
	if !ok {
		return nil
	}
	key := f.export + "/" + f.root + "/" + strings.Join(parts, "/")
	h.mu.Lock()
	defer h.mu.Unlock()
	if id, ok := h.reverse[key]; ok {
		return []byte(id)
	}
	if len(h.handles) >= 4096 {
		return nil
	}
	id, err := randomKey(16)
	if err != nil {
		return nil
	}
	h.handles[id] = nfsRef{f, append([]string(nil), parts...)}
	h.reverse[key] = id
	return []byte(id)
}
func (h *logicalNFS) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	h.mu.Lock()
	r, ok := h.handles[string(handle)]
	h.mu.Unlock()
	if !ok {
		return nil, nil, os.ErrNotExist
	}
	r.fs.a.mu.Lock()
	err := r.fs.check()
	r.fs.a.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return r.fs, append([]string(nil), r.parts...), nil

}
func (h *logicalNFS) InvalidateHandle(fs billy.Filesystem, handle []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.handles, string(handle))
	for k, v := range h.reverse {
		if v == string(handle) {
			delete(h.reverse, k)
		}
	}
	return nil
}

type logicalFS struct {
	a            *App
	owner        int64
	export, root string
}
type logicalNode struct {
	id, parent, name, key string
	size, created         int64
	dir                   bool
	mode, modified        int64
}
type logicalInfo struct{ n logicalNode }

func (i logicalInfo) Name() string { return i.n.name }
func (i logicalInfo) Size() int64  { return i.n.size }
func (i logicalInfo) Mode() os.FileMode {
	if i.n.dir {
		return os.ModeDir | os.FileMode(i.n.mode)
	}
	return os.FileMode(i.n.mode)
}
func (i logicalInfo) ModTime() time.Time {
	if i.n.modified != 0 {
		return time.Unix(i.n.modified, 0)
	}
	return time.Unix(i.n.created, 0)
}
func (i logicalInfo) IsDir() bool { return i.n.dir }
func (i logicalInfo) Sys() any    { return nil }
func (f *logicalFS) check() error {
	var n int
	err := f.a.db.QueryRow(`SELECT 1 FROM nfs_exports WHERE id=? AND user_id=? AND expires_at>?`, f.export, f.owner, time.Now().Unix()).Scan(&n)
	if err != nil {
		return os.ErrPermission
	}
	return nil
}
func logicalPath(v string) ([]string, error) {
	if strings.ContainsAny(v, "\\\x00:") {
		return nil, os.ErrPermission
	}
	out := []string{}
	for _, s := range strings.Split(v, "/") {
		if s == "" || s == "." {
			continue
		}
		if s == ".." || !validName(s) {
			return nil, os.ErrPermission
		}
		out = append(out, s)
	}
	if len(out) > 128 {
		return nil, os.ErrPermission
	}
	return out, nil
}
func (f *logicalFS) resolve(v string) (logicalNode, error) {
	if err := f.check(); err != nil {
		return logicalNode{}, err
	}
	parts, err := logicalPath(v)
	if err != nil {
		return logicalNode{}, err
	}
	n := logicalNode{id: f.root, name: "/", dir: true, created: 1, mode: 0700}
	if f.root != "" {
		g, e := getFolder(context.Background(), f.a.db, f.owner, f.root)
		if e != nil {
			return n, os.ErrNotExist
		}
		n.name = g.Name
		n.created = g.CreatedAt
	}
	for _, seg := range parts {
		if !n.dir {
			return n, os.ErrNotExist
		}
		parent := n.id
		rows, e := f.a.db.Query(`SELECT id,name,0,created_at,'',1,nfs_mode,nfs_mtime FROM folders WHERE user_id=? AND COALESCE(parent_id,'')=? AND name=? UNION ALL SELECT f.id,f.name,f.size,f.created_at,b.storage_key,0,f.nfs_mode,f.nfs_mtime FROM files f JOIN blobs b ON f.blob_id=b.id WHERE f.user_id=? AND COALESCE(f.folder_id,'')=? AND f.name=? AND f.deleted=0`, f.owner, parent, seg, f.owner, parent, seg)
		if e != nil {
			return n, e
		}
		found := 0
		for rows.Next() {
			var dir int
			if e = rows.Scan(&n.id, &n.name, &n.size, &n.created, &n.key, &dir, &n.mode, &n.modified); e != nil {
				rows.Close()
				return n, e
			}
			n.dir = dir == 1
			n.parent = parent
			found++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return n, e
		}
		if found == 0 {
			return n, os.ErrNotExist
		}
		if found > 1 {
			return n, errors.New("ambiguous logical name; rename duplicate entries using the web UI")
		}
	}
	return n, nil
}
func (f *logicalFS) Stat(v string) (os.FileInfo, error) {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	if err != nil {
		return nil, err
	}
	return logicalInfo{n}, nil
}
func (f *logicalFS) Lstat(v string) (os.FileInfo, error) { return f.Stat(v) }
func (f *logicalFS) Root() string                        { return "/" }
func (f *logicalFS) Join(parts ...string) string         { return strings.Join(parts, "/") }
func (f *logicalFS) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.WriteCapability | billy.ReadAndWriteCapability | billy.SeekCapability | billy.TruncateCapability
}
func (f *logicalFS) Chroot(v string) (billy.Filesystem, error) {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	if err != nil {
		return nil, err
	}
	if !n.dir {
		return nil, os.ErrInvalid
	}
	return &logicalFS{f.a, f.owner, f.export, n.id}, nil
}
func (f *logicalFS) ReadDir(v string) ([]os.FileInfo, error) {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	if err != nil {
		return nil, err
	}
	if !n.dir {
		return nil, os.ErrInvalid
	}
	rows, err := f.a.db.Query(`SELECT id,name,0,created_at,1,nfs_mode,nfs_mtime FROM folders WHERE user_id=? AND COALESCE(parent_id,'')=? UNION ALL SELECT id,name,size,created_at,0,nfs_mode,nfs_mtime FROM files WHERE user_id=? AND COALESCE(folder_id,'')=? AND deleted=0`, f.owner, n.id, f.owner, n.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []os.FileInfo{}
	seen := map[string]bool{}
	for rows.Next() {
		var x logicalNode
		var dir int
		if err = rows.Scan(&x.id, &x.name, &x.size, &x.created, &dir, &x.mode, &x.modified); err != nil {
			return nil, err
		}
		x.dir = dir == 1
		if seen[x.name] {
			return nil, errors.New("ambiguous logical names")
		}
		seen[x.name] = true
		out = append(out, logicalInfo{x})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}
func (f *logicalFS) MkdirAll(v string, perm os.FileMode) error {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	parts, err := logicalPath(v)
	if err != nil {
		return err
	}
	if err = f.check(); err != nil {
		return err
	}
	parent := f.root
	for i, name := range parts {
		n, e := f.resolve(strings.Join(parts[:i+1], "/"))
		if e == nil {
			if !n.dir {
				return os.ErrExist
			}
			parent = n.id
			continue
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if err = checkParent(context.Background(), f.a.db, f.owner, parent); err != nil {
			return err
		}
		id, e := randomKey(16)
		if e != nil {
			return e
		}
		_, e = f.a.db.Exec(`INSERT INTO folders(id,user_id,name,parent_id,created_at) VALUES(?,?,?,?,?)`, id, f.owner, name, nullableID(parent), time.Now().Unix())
		if e != nil {
			return e
		}
		parent = id
	}
	return nil
}
func (f *logicalFS) Remove(v string) error {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	if err != nil {
		return err
	}
	if n.id == f.root {
		return os.ErrPermission
	}
	if n.dir {
		var count int
		err = f.a.db.QueryRow(`SELECT (SELECT count(*) FROM folders WHERE parent_id=?)+(SELECT count(*) FROM files WHERE folder_id=?)+(SELECT count(*) FROM upload_sessions WHERE folder_id=?)`, n.id, n.id, n.id).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return errors.New("directory not empty")
		}
		_, err = f.a.db.Exec(`DELETE FROM folders WHERE id=? AND user_id=?`, n.id, f.owner)
		return err
	}
	tx, err := f.a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = releaseFile(tx, n.id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return f.a.gcBlobs(context.Background())
}
func (f *logicalFS) Rename(old, new string) error {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(old)
	if err != nil {
		return err
	}
	if n.id == f.root {
		return os.ErrPermission
	}
	parts, err := logicalPath(new)
	if err != nil || len(parts) == 0 {
		return os.ErrPermission
	}
	parent, err := f.resolve(strings.Join(parts[:len(parts)-1], "/"))
	if err != nil || !parent.dir {
		return os.ErrNotExist
	}
	name := parts[len(parts)-1]
	if old == new {
		return nil
	}
	if existing, e := f.resolve(new); e == nil {
		if existing.id == n.id {
			return nil
		}
		return os.ErrExist
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if n.dir {
		var count int
		err = f.a.db.QueryRow(`WITH RECURSIVE tree(id) AS (SELECT id FROM folders WHERE id=? AND user_id=? UNION SELECT x.id FROM folders x JOIN tree t ON x.parent_id=t.id WHERE x.user_id=?) SELECT count(*) FROM tree WHERE id=?`, n.id, f.owner, f.owner, parent.id).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return os.ErrPermission
		}
		_, err = f.a.db.Exec(`UPDATE folders SET name=?,parent_id=? WHERE id=? AND user_id=?`, name, nullableID(parent.id), n.id, f.owner)
	} else {
		_, err = f.a.db.Exec(`UPDATE files SET name=?,folder_id=? WHERE id=? AND user_id=?`, name, nullableID(parent.id), n.id, f.owner)
	}
	return err
}
func (f *logicalFS) Open(v string) (billy.File, error) { return f.OpenFile(v, os.O_RDONLY, 0600) }
func (f *logicalFS) Create(v string) (billy.File, error) {
	return f.OpenFile(v, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
}
func (f *logicalFS) OpenFile(v string, flag int, perm os.FileMode) (billy.File, error) {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	write := flag&(os.O_WRONLY|os.O_RDWR) != 0
	if errors.Is(err, os.ErrNotExist) && flag&os.O_CREATE != 0 {
		parts, e := logicalPath(v)
		if e != nil || len(parts) == 0 {
			return nil, os.ErrPermission
		}
		parent, e := f.resolve(strings.Join(parts[:len(parts)-1], "/"))
		if e != nil || !parent.dir {
			return nil, os.ErrNotExist
		}
		n = logicalNode{parent: parent.id, name: parts[len(parts)-1]}
		err = nil
	} else if err == nil && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	if err != nil {
		return nil, err
	}
	if n.dir {
		return nil, os.ErrInvalid
	}
	key, err := randomKey(16)
	if err != nil {
		return nil, err
	}
	scratch, err := os.OpenFile(filepath.Join(f.a.temp, key), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			scratch.Close()
			os.Remove(scratch.Name())
		}
	}()
	if n.id != "" && flag&os.O_TRUNC == 0 {
		src, e := f.a.openBlob(context.Background(), n.key)
		if e != nil {
			return nil, e
		}
		_, e = io.CopyBuffer(scratch, src, make([]byte, 32768))
		src.Close()
		if e != nil {
			return nil, e
		}
	}
	if flag&os.O_APPEND != 0 {
		_, err = scratch.Seek(0, io.SeekEnd)
	} else {
		_, err = scratch.Seek(0, io.SeekStart)
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return &logicalFile{File: scratch, fs: f, node: n, name: v, write: write, dirty: write && (n.id == "" || flag&os.O_TRUNC != 0)}, nil
}

type logicalFile struct {
	*os.File
	fs                   *logicalFS
	node                 logicalNode
	name                 string
	write, dirty, closed bool
}

func (f *logicalFile) Name() string { return f.name }
func (f *logicalFile) Read(b []byte) (int, error) {
	f.fs.a.mu.Lock()
	defer f.fs.a.mu.Unlock()
	if err := f.fs.check(); err != nil {
		return 0, err
	}
	return f.File.Read(b)
}
func (f *logicalFile) ReadAt(b []byte, off int64) (int, error) {
	f.fs.a.mu.Lock()
	defer f.fs.a.mu.Unlock()
	if err := f.fs.check(); err != nil {
		return 0, err
	}
	return f.File.ReadAt(b, off)
}
func (f *logicalFile) Write(b []byte) (int, error) {
	if !f.write {
		return 0, os.ErrPermission
	}
	off, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if off > f.fs.a.cfg.MaxUploadBytes-int64(len(b)) {
		return 0, errors.New("NFS upload limit exceeded")
	}
	f.dirty = true
	return f.File.Write(b)
}
func (f *logicalFile) Truncate(size int64) error {
	if !f.write || size < 0 || size > f.fs.a.cfg.MaxUploadBytes {
		return os.ErrPermission
	}
	f.dirty = true
	return f.File.Truncate(size)
}
func (f *logicalFile) Lock() error   { return billy.ErrNotSupported }
func (f *logicalFile) Unlock() error { return billy.ErrNotSupported }
func (f *logicalFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	defer os.Remove(f.File.Name())
	defer f.File.Close()
	if !f.dirty {
		return nil
	}
	f.fs.a.mu.Lock()
	defer f.fs.a.mu.Unlock()
	if err := f.fs.check(); err != nil {
		return err
	}
	if f.node.id != "" {
		n, err := f.fs.resolve(f.name)
		if err != nil {
			return err
		}
		if n.id != f.node.id || n.key != f.node.key {
			return errors.New("concurrent logical file modification; retry")
		}
	}
	info, err := f.File.Stat()
	if err != nil {
		return err
	}
	if info.Size() > f.fs.a.cfg.MaxUploadBytes {
		return os.ErrPermission
	}
	if _, err = f.File.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := sha256.New()
	if _, err = io.CopyBuffer(digest, f.File, make([]byte, 32768)); err != nil {
		return err
	}
	if err = f.File.Sync(); err != nil {
		return err
	}
	if err = f.File.Close(); err != nil {
		return err
	}
	_, err = f.fs.a.publishReplacing(context.Background(), f.fs.owner, f.node.name, f.node.parent, info.Size(), hex.EncodeToString(digest.Sum(nil)), f.File.Name(), f.node.id)
	if err != nil {
		return err
	}
	return f.fs.a.gcBlobs(context.Background())
}
func (f *logicalFS) TempFile(dir, prefix string) (billy.File, error) {
	key, err := randomKey(8)
	if err != nil {
		return nil, err
	}
	return f.Create(path.Join(dir, prefix+key))
}
func (f *logicalFS) Symlink(target, link string) error { return os.ErrPermission }
func (f *logicalFS) Readlink(v string) (string, error) { return "", os.ErrPermission }

// Modes/times are logical metadata. UID/GID cannot choose an app identity.
func (f *logicalFS) Chmod(v string, mode os.FileMode) error {
	return f.setAttribute(v, "nfs_mode", int64(mode.Perm()))
}
func (f *logicalFS) Chown(v string, uid, gid int) error {
	if (uid != 0 && uid != -1) || (gid != 0 && gid != -1) {
		return os.ErrPermission
	}
	_, err := f.Stat(v)
	return err
}
func (f *logicalFS) Lchown(v string, uid, gid int) error { return f.Chown(v, uid, gid) }
func (f *logicalFS) Chtimes(v string, at, mt time.Time) error {
	return f.setAttribute(v, "nfs_mtime", mt.Unix())
}
func (f *logicalFS) setAttribute(v, column string, value int64) error {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	n, err := f.resolve(v)
	if err != nil {
		return err
	}
	if n.id == "" {
		return os.ErrPermission
	}
	table := "files"
	if n.dir {
		table = "folders"
	}
	_, err = f.a.db.Exec("UPDATE "+table+" SET "+column+"=? WHERE id=? AND user_id=?", value, n.id, f.owner)
	return err
}
