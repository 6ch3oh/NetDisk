package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type RemoteFolder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parent_id"`
	CreatedAt int64  `json:"created_at"`
}
type RemoteFile struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	FolderID  string         `json:"folder_id"`
	Size      int64          `json:"size"`
	CreatedAt int64          `json:"created_at"`
	SHA256    string         `json:"sha256"`
	Folders   []RemoteFolder `json:"folders"`
}
type Manifest struct {
	Version int            `json:"version"`
	Files   []RemoteFile   `json:"files"`
	Folders []RemoteFolder `json:"folders"`
}
type Archive struct {
	ID             string     `json:"id"`
	File           RemoteFile `json:"file"`
	DiskID         string     `json:"disk_id"`
	DiskLabel      string     `json:"disk_label"`
	LocalPath      string     `json:"local_path"`
	ArchivedAt     int64      `json:"archived_at"`
	RestoredFileID string     `json:"restored_file_id"`
	CleanupPending bool       `json:"cleanup_pending,omitempty"`
}

func validRemoteName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && len(s) <= 1020
}
func validRemoteFile(f RemoteFile) bool {
	if !idPattern.MatchString(f.ID) || !hashPattern.MatchString(f.SHA256) || f.Size < 0 || f.Size > 1<<50 || !validRemoteName(f.Name) || len(f.Folders) > 256 {
		return false
	}
	parent := ""
	seen := map[string]bool{}
	for _, d := range f.Folders {
		if !idPattern.MatchString(d.ID) || seen[d.ID] || d.ParentID != parent || !validRemoteName(d.Name) {
			return false
		}
		seen[d.ID] = true
		parent = d.ID
	}
	return parent == f.FolderID
}
func fileRevision(f RemoteFile) string { b, _ := json.Marshal(f); return sum(string(b)) }
func (c *Client) Manifest(ctx context.Context) (Manifest, error) {
	var m Manifest
	if err := c.request(ctx, "GET", "/api/backup/manifest", nil, &m); err != nil {
		return m, err
	}
	if m.Version != 1 || m.Files == nil || m.Folders == nil {
		return m, errors.New("server does not provide a supported backup manifest")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !validRemoteFile(f) || seen[f.ID] {
			return m, errors.New("invalid file manifest")
		}
		seen[f.ID] = true
	}
	dirs := map[string]RemoteFolder{}
	for _, d := range m.Folders {
		if !idPattern.MatchString(d.ID) || !validRemoteName(d.Name) {
			return m, errors.New("invalid folder manifest")
		}
		if _, ok := dirs[d.ID]; ok {
			return m, errors.New("duplicate folder")
		}
		dirs[d.ID] = d
	}
	for _, d := range m.Folders {
		visited := map[string]bool{}
		for id := d.ID; id != ""; {
			v, ok := dirs[id]
			if !ok || visited[id] || len(visited) >= 256 {
				return m, errors.New("invalid folder tree")
			}
			visited[id] = true
			id = v.ParentID
		}
	}
	return m, nil
}

// Each bounded range is independently retriable. Signed object URLs and API
// cookies never enter errors or persistent state, and redirects use no cookie jar.
func (c *Client) downloadRange(ctx context.Context, f RemoteFile, start, end int64) (*http.Response, error) {
	get := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", c.Endpoint+"/api/files/"+f.ID+"/download", nil)
		if err != nil {
			return nil, errors.New("invalid download request")
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, errors.New("download transport failure")
		}
		return resp, nil
	}
	resp, err := get()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		c.auth.Lock()
		err = c.login(ctx)
		c.auth.Unlock()
		if err != nil {
			return nil, err
		}
		resp, err = get()
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode == 307 {
		target := resp.Header.Get("Location")
		resp.Body.Close()
		if !validURL(target) {
			return nil, errors.New("unsafe object download redirect")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return nil, errors.New("invalid object download redirect")
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set("Accept-Encoding", "identity")
		hc := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err = hc.Do(req)
		if err != nil {
			return nil, errors.New("object download transport failure")
		}
	}
	want := fmt.Sprintf("bytes %d-%d/%d", start, end, f.Size)
	if resp.StatusCode != 206 {
		resp.Body.Close()
		return nil, &HTTPError{resp.StatusCode}
	}
	if resp.Header.Get("Content-Range") != want || resp.ContentLength >= 0 && resp.ContentLength != end-start+1 || resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		resp.Body.Close()
		return nil, errors.New("download range mismatch")
	}
	return resp, nil
}
func (c *Client) archive(ctx context.Context, v Archive) (Archive, error) {
	b, _ := json.Marshal(v)
	var out Archive
	err := c.request(ctx, "POST", "/api/archives", b, &out)
	if err == nil && (out.ID != v.ID || out.DiskID != v.DiskID || out.LocalPath != v.LocalPath || fileRevision(out.File) != fileRevision(v.File) || out.ArchivedAt <= 0) {
		err = errors.New("invalid archive acknowledgement")
	}
	return out, err
}

func (c *Client) restoreUpload(ctx context.Context, source io.ReaderAt, v PullEntry, folder string, partBytes int64) (File, error) {
	e := Entry{RemoteName: v.File.Name, FolderID: folder, Size: v.File.Size, Hash: v.File.SHA256, Key: v.RestoreKey}
	s, err := c.create(ctx, e, partBytes)
	if err != nil {
		return File{}, err
	}
	if s.File != nil {
		if s.File.Name != e.RemoteName || s.File.Size != e.Size {
			return File{}, errors.New("restore receipt mismatch")
		}
		return *s.File, c.verify(ctx, *s.File, e.Hash)
	}
	parts, err := c.parts(ctx, s.ID)
	if err != nil {
		return File{}, err
	}
	saved := map[int64]Part{}
	for _, p := range parts {
		saved[p.Index] = p
	}
	h := sha256.New()
	buf := make([]byte, partBytes)
	for i, offset := int64(0), int64(0); offset < e.Size; i++ {
		n := min(partBytes, e.Size-offset)
		chunk := buf[:n]
		if _, err = io.ReadFull(io.NewSectionReader(source, offset, n), chunk); err != nil {
			return File{}, errors.New("backup read failed")
		}
		h.Write(chunk)
		digest := sha256.Sum256(chunk)
		if p, ok := saved[i]; ok {
			if p.Size != n || p.Hash != hex.EncodeToString(digest[:]) {
				return File{}, errors.New("restore part conflict")
			}
		} else if err = c.request(ctx, "PUT", fmt.Sprintf("/api/uploads/%s/parts/%d", s.ID, i), chunk, nil); err != nil {
			return File{}, err
		}
		offset += n
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Hash {
		return File{}, errors.New("backup changed during restore; not published")
	}
	var result File
	if err = c.request(ctx, "POST", "/api/uploads/"+s.ID+"/complete", nil, &result); err != nil {
		return File{}, err
	}
	if !idPattern.MatchString(result.ID) || result.Name != e.RemoteName || result.Size != e.Size {
		return File{}, errors.New("invalid restoration response")
	}
	return result, nil
}
