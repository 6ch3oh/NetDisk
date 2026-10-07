// Package backup implements one-way versioned backups to the existing NetDisk API.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile("^[a-f0-9]{32}$")
var hashPattern = regexp.MustCompile("^[a-f0-9]{64}$")

type HTTPError struct{ Code int }

func (e *HTTPError) Error() string     { return fmt.Sprintf("NetDisk HTTP %d", e.Code) }
func hasCode(err error, code int) bool { var e *HTTPError; return errors.As(err, &e) && e.Code == code }

type Client struct {
	Endpoint           string
	UserID             int64
	MaxUploadBytes     int64
	http               *http.Client
	auth               sync.Mutex
	username, password string // Memory only. Never serialized or logged.
}
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Session struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FolderID  string `json:"folder_id"`
	Size      int64  `json:"expected_size"`
	PartBytes int64  `json:"part_size"`
	File      *File  `json:"file,omitempty"`
}
type Part struct {
	Index int64  `json:"index"`
	Size  int64  `json:"size"`
	Hash  string `json:"sha256"`
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil &&
		(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}
func NewClient(ctx context.Context, endpoint, username, password string) (*Client, error) {
	endpoint = strings.TrimRight(endpoint, "/")
	u, err := url.Parse(endpoint)
	if !validURL(endpoint) || err != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("server must be an HTTPS origin or loopback HTTP origin, without credentials or a path")
	}
	jar, _ := cookiejar.New(nil)
	c := &Client{Endpoint: endpoint, username: username, password: password, http: &http.Client{
		Jar: jar, Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	var cfg struct {
		Limit int64 `json:"max_upload_bytes"`
	}
	if err := c.request(ctx, "GET", "/api/config", nil, &cfg); err != nil {
		return nil, err
	}
	if cfg.Limit <= 0 {
		return nil, errors.New("invalid server upload limit")
	}
	c.MaxUploadBytes = cfg.Limit
	return c, nil
}
func (c *Client) login(ctx context.Context) error {
	data, _ := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	resp, err := c.send(ctx, "POST", "/api/login", data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &HTTPError{resp.StatusCode}
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return errors.New("invalid login response")
	}
	if user.ID <= 0 || c.UserID != 0 && c.UserID != user.ID {
		return errors.New("login identity changed")
	}
	c.UserID = user.ID
	return nil
}
func (c *Client) send(ctx context.Context, method, path string, data []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("could not construct API request")
	}
	req.Header.Set("X-NetDisk-Request", "1")
	if strings.Contains(path, "/parts/") {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("NetDisk transport failure") // URLs and cookies must not enter errors.
	}
	return resp, nil
}
func (c *Client) response(ctx context.Context, method, path string, data []byte) (*http.Response, error) {
	resp, err := c.send(ctx, method, path, data)
	if err != nil || resp.StatusCode != 401 {
		return resp, err
	}
	resp.Body.Close()
	c.auth.Lock()
	err = c.login(ctx)
	c.auth.Unlock()
	if err != nil {
		return nil, err
	}
	return c.send(ctx, method, path, data) // At most one re-login per request; no unbounded network retry.
}
func (c *Client) request(ctx context.Context, method, path string, data []byte, out any) error {
	resp, err := c.response(ctx, method, path, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{resp.StatusCode}
	}
	if out != nil && resp.StatusCode != 204 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
			return errors.New("invalid API response")
		}
	}
	return nil
}
func (c *Client) directory(ctx context.Context, id string) ([]Folder, []File, error) {
	var data struct {
		Folders []Folder `json:"folders"`
		Files   []File   `json:"files"`
	}
	err := c.request(ctx, "GET", "/api/directory?folder_id="+url.QueryEscape(id), nil, &data)
	return data.Folders, data.Files, err
}
func (c *Client) ensureFolder(ctx context.Context, parent, name string) (string, error) {
	for n := 0; n < 2; n++ {
		folders, _, err := c.directory(ctx, parent)
		if err != nil {
			return "", err
		}
		for _, f := range folders {
			if f.Name == name && idPattern.MatchString(f.ID) {
				return f.ID, nil
			}
		}
		data, _ := json.Marshal(map[string]string{"name": name, "parent_id": parent})
		var f Folder
		err = c.request(ctx, "POST", "/api/folders", data, &f)
		if err == nil {
			if !idPattern.MatchString(f.ID) {
				return "", errors.New("invalid folder response")
			}
			return f.ID, nil
		}
		if !hasCode(err, 409) {
			return "", err
		}
	}
	return "", errors.New("folder conflict")
}
func (c *Client) create(ctx context.Context, e Entry, partBytes int64) (Session, error) {
	data, _ := json.Marshal(map[string]any{"name": e.RemoteName, "folder_id": e.FolderID, "expected_size": e.Size, "part_size": partBytes, "request_key": e.Key})
	var s Session
	err := c.request(ctx, "POST", "/api/uploads", data, &s)
	if err == nil && (!idPattern.MatchString(s.ID) || s.Name != e.RemoteName || s.FolderID != e.FolderID || s.Size != e.Size || s.PartBytes != partBytes) {
		err = errors.New("upload session does not match persistent state")
	}
	return s, err
}
func (c *Client) parts(ctx context.Context, id string) ([]Part, error) {
	var result struct {
		Parts []Part `json:"parts"`
	}
	err := c.request(ctx, "GET", "/api/uploads/"+id, nil, &result)
	return result.Parts, err
}
func (c *Client) cancel(ctx context.Context, id string) error {
	err := c.request(ctx, "DELETE", "/api/uploads/"+id, nil, nil)
	if hasCode(err, 404) {
		return nil
	}
	return err
}

// Verification follows object-store redirects using a separate client with no
// cookie jar or API headers. A loopback MinIO URL therefore never receives the
// NetDisk session cookie, even if it uses the same hostname on another port.
func (c *Client) verify(ctx context.Context, f File, hash string) error {
	if !idPattern.MatchString(f.ID) {
		return errors.New("invalid remote file ID")
	}
	resp, err := c.response(ctx, "GET", "/api/files/"+f.ID+"/download", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == 307 {
		target := resp.Header.Get("Location")
		resp.Body.Close()
		if !validURL(target) {
			return errors.New("unsafe object download redirect")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return errors.New("invalid object download redirect")
		}
		hc := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err = hc.Do(req)
		if err != nil {
			return errors.New("object verification transport failure")
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &HTTPError{resp.StatusCode}
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(resp.Body, f.Size+1), make([]byte, 128<<10))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != hash {
		return errors.New("remote version content mismatch")
	}
	return nil
}
