package netdisk

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/minio/minio-go/v7"
	s3credentials "github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint, DownloadEndpoint, AccessKey, SecretKey, Bucket string
	Secure                                                   bool
}
type objectStore struct {
	client, download *minio.Client
	bucket           string
}

func newObjectStore(cfg S3Config) (*objectStore, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("incomplete object storage configuration")
	}
	opts := &minio.Options{Creds: s3credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.Secure, Region: "us-east-1"}
	c, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, errors.New("invalid object storage endpoint")
	}
	download := c
	if cfg.DownloadEndpoint != "" {
		download, err = minio.New(cfg.DownloadEndpoint, opts)
		if err != nil {
			return nil, errors.New("invalid object download endpoint")
		}
	}
	return &objectStore{c, download, cfg.Bucket}, nil
}
func (s *objectStore) remove(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
func (a *App) serveBlob(w http.ResponseWriter, r *http.Request, f File, key string) {
	var backend string
	if err := a.db.QueryRowContext(r.Context(), `SELECT backend FROM blobs WHERE storage_key=?`, key).Scan(&backend); err != nil {
		fileError(w, err)
		return
	}
	if r.Method == "GET" {
		if _, err := a.db.ExecContext(r.Context(), `UPDATE blobs SET access_count=access_count+1,last_access=? WHERE storage_key=?`, time.Now().Unix(), key); err != nil {
			fileError(w, err)
			return
		}
	}
	if backend == "s3" {
		if a.store == nil {
			fail(w, 503, "object storage unavailable")
			return
		}
		params := make(url.Values)
		params.Set("response-content-type", "application/octet-stream")
		params.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
		target, err := a.store.download.PresignedGetObject(r.Context(), a.store.bucket, key, 2*time.Minute, params)
		if err != nil {
			fail(w, 503, "object storage unavailable")
			return
		}
		// HEAD validates the same download capability without issuing a signed GET
		// URL to a HEAD client (S3 signatures bind the HTTP method).
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Location", target.String())
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	source, err := os.Open(filepath.Join(a.blobs, key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fileError(w, sql.ErrNoRows)
		} else {
			fileError(w, err)
		}
		return
	}
	defer source.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	http.ServeContent(w, r, f.Name, time.Unix(f.CreatedAt, 0), source)
}
func (a *App) openBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	var backend string
	if err := a.db.QueryRowContext(ctx, `SELECT backend FROM blobs WHERE storage_key=?`, key).Scan(&backend); err != nil {
		return nil, err
	}
	if backend == "s3" {
		if a.store == nil {
			return nil, errors.New("object storage unavailable")
		}
		return a.store.client.GetObject(ctx, a.store.bucket, key, minio.GetObjectOptions{})
	}
	return os.Open(filepath.Join(a.blobs, key))
}
func (a *App) migrateObject(w http.ResponseWriter, r *http.Request) {
	if a.cfg.MaintenanceKey == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-NetDisk-Maintenance")), []byte(a.cfg.MaintenanceKey)) != 1 {
		fail(w, 403, "maintenance authorization required")
		return
	}
	if a.store == nil {
		fail(w, 503, "object storage unavailable")
		return
	}
	f, key, err := a.owned(r)
	if err != nil {
		fileError(w, err)
		return
	}
	if err = a.migrateBlob(r.Context(), key); err != nil {
		fail(w, 503, "object migration failed; local content preserved or cleanup pending")
		return
	}
	respond(w, 200, map[string]any{"id": f.ID, "backend": "s3"})
}
func (a *App) migrateBlob(ctx context.Context, key string) error {
	var backend, hash string
	var size int64
	if err := a.db.QueryRowContext(ctx, `SELECT backend,content_hash,size FROM blobs WHERE storage_key=?`, key).Scan(&backend, &hash, &size); err != nil {
		return err
	}
	if backend == "s3" {
		return a.cleanupKeys(ctx, "local_cleanup", a.blobs)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	source, err := os.Open(filepath.Join(a.blobs, key))
	if err != nil {
		return err
	}
	// Record an uncommitted remote copy before uploading. A crash or failed
	// verification cannot make that copy unreachable to later cleanup.
	if _, err = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO object_cleanup VALUES(?)`, key); err != nil {
		source.Close()
		return err
	}
	digest := sha256.New()
	_, err = a.store.client.PutObject(ctx, a.store.bucket, key, io.TeeReader(source, digest), size, minio.PutObjectOptions{ContentType: "application/octet-stream", UserMetadata: map[string]string{"content-sha256": hash}})
	source.Close()
	if err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != hash {
		return errors.New("local blob hash verification failed")
	}
	info, err := a.store.client.StatObject(ctx, a.store.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return err
	}
	if info.Size != size || info.UserMetadata["Content-Sha256"] != hash {
		return errors.New("object metadata verification failed")
	}
	// Verify stored bytes independently of our own user metadata. Full reads
	// occur only during migration/ZIP/NFS, never on ordinary download routes.
	remote, err := a.store.client.GetObject(ctx, a.store.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	remoteHash := sha256.New()
	remoteSize, err := io.CopyBuffer(remoteHash, io.LimitReader(remote, size+1), make([]byte, 32768))
	remote.Close()
	if err != nil {
		return err
	}
	if remoteSize != size || hex.EncodeToString(remoteHash.Sum(nil)) != hash {
		return errors.New("stored object hash verification failed")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE blobs SET backend='s3' WHERE storage_key=? AND backend='local'`, key)
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM object_cleanup WHERE storage_key=?`, key)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO local_cleanup VALUES(?)`, key)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	return a.cleanupKeys(ctx, "local_cleanup", a.blobs)
}
