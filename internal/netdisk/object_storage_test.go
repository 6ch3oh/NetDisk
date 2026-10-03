package netdisk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	s3credentials "github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/crypto/bcrypt"
)

type corruptMetadataTransport struct{ inner http.RoundTripper }

func (t corruptMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.inner.RoundTrip(r)
	if err == nil && r.Method == "HEAD" && response.StatusCode == 200 {
		response.Header.Set("X-Amz-Meta-Content-Sha256", "invalid-synthetic-hash")
	}
	return response, err
}
func TestObjectRedirectDoesNotReadBodyAndMigrationFailure(t *testing.T) {
	// A request-counting upstream proves signing a download never reads content.
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer upstream.Close()
	endpoint, _ := url.Parse(upstream.URL)
	a, err := New(Config{DataDir: t.TempDir(), Origin: "http://127.0.0.1:38120", MaxUploadBytes: 4096, BcryptCost: bcrypt.MinCost, S3: &S3Config{Endpoint: endpoint.Host, AccessKey: "synthetic-access", SecretKey: "synthetic-secret", Bucket: "synthetic-bucket"}, MaintenanceKey: "synthetic-maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := &harness{t, a, a.Handler()}
	c := h.account("object_unit")
	payload := randomContent(t, 512)
	f := putFile(t, h, c, "object.bin", "", payload)
	var key string
	a.db.QueryRow(`SELECT storage_key FROM files WHERE id=?`, f.ID).Scan(&key)
	status(t, h.request("POST", "/api/files/"+f.ID+"/migrate-s3", "", "", c), 403)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err = a.migrateBlob(ctx, key); err == nil {
		t.Fatal("failed upstream marked migrated")
	}
	var backend string
	a.db.QueryRow(`SELECT backend FROM blobs`).Scan(&backend)
	if backend != "local" {
		t.Fatal("migration switched metadata on failure")
	}
	sameContent(t, h.request("GET", f.DownloadURL, "", "", c).Body.Bytes(), payload)
	if _, err = a.db.Exec(`UPDATE blobs SET backend='s3'`); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	w := h.request("GET", f.DownloadURL, "", "", c)
	status(t, w, 307)
	if w.Body.Len() != 0 || calls.Load() != before {
		t.Fatal("app proxied object body or contacted object store for signing")
	}
	signed, err := url.Parse(w.Header().Get("Location"))
	if err != nil || signed.Query().Get("X-Amz-Expires") != "120" || signed.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("missing short-lived signature")
	}
}
func TestMinIOMigrationRedirectRangeDeletionAndObjectZIP(t *testing.T) {
	endpoint := os.Getenv("NETDISK_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("NOT_RUN: set NETDISK_TEST_MINIO_ENDPOINT using isolated MinIO test profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	suffix, err := randomKey(8)
	if err != nil {
		t.Fatal(err)
	}
	bucket := "netdisk-test-" + suffix
	cfg := Config{DataDir: t.TempDir(), Origin: "http://127.0.0.1:38120", MaxUploadBytes: 1 << 20, BcryptCost: bcrypt.MinCost, S3: &S3Config{Endpoint: endpoint, AccessKey: "netdisk-test-only", SecretKey: "netdisk-test-only-password", Bucket: bucket}, MaintenanceKey: "synthetic-maintenance"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Close() }()
	store := a.store
	// The server can still be starting; bounded health/bucket readiness attempts.
	for attempt := 0; attempt < 30; attempt++ {
		err = store.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("isolated MinIO readiness timeout")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err != nil {
		t.Fatal("isolated MinIO bucket unavailable")
	}
	defer func() {
		for obj := range store.client.ListObjects(context.Background(), bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err == nil {
				_ = store.remove(context.Background(), obj.Key)
			}
		}
		_ = store.client.RemoveBucket(context.Background(), bucket)
	}()
	h := &harness{t, a, a.Handler()}
	userA := h.account("minio_a")
	userB := h.account("minio_b")
	folder := makeFolder(t, h, userB, "object-folder", "")
	payload := randomContent(t, 128<<10)
	fa := putFile(t, h, userA, "a.bin", "", payload)
	fb := putFile(t, h, userB, "b.bin", folder.ID, payload)
	var key string
	if err = a.db.QueryRow(`SELECT storage_key FROM blobs`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	// Verification failure after a successful PUT must leave local metadata/readability.
	missingBucket := store.bucket
	store.bucket = "nonexistent-" + suffix
	err = a.migrateBlob(ctx, key)
	store.bucket = missingBucket
	if err == nil {
		t.Fatal("missing bucket migration unexpectedly succeeded")
	}
	sameContent(t, h.request("GET", fb.DownloadURL, "", "", userB).Body.Bytes(), payload)
	// Inject a metadata verification error against the real server, after PUT.
	originalClient := store.client
	badClient, err := minio.New(endpoint, &minio.Options{Creds: s3credentials.NewStaticV4("netdisk-test-only", "netdisk-test-only-password", ""), Region: "us-east-1", Transport: corruptMetadataTransport{http.DefaultTransport}})
	if err != nil {
		t.Fatal(err)
	}
	store.client = badClient
	err = a.migrateBlob(ctx, key)
	store.client = originalClient
	if err == nil {
		t.Fatal("invalid object metadata accepted")
	}
	if countQuery(t, a, `SELECT count(*) FROM object_cleanup WHERE storage_key=?`, key) != 1 {
		t.Fatal("uncommitted remote copy not tracked")
	}
	sameContent(t, h.request("GET", fb.DownloadURL, "", "", userB).Body.Bytes(), payload)
	if err = a.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{}); minio.ToErrorResponse(err).Code != "NoSuchKey" {
		t.Fatal("failed migration remote copy not cleaned")
	}
	// A metadata transaction failure after successful object verification is safe.
	if _, err = a.db.Exec(`CREATE TRIGGER reject_object_switch BEFORE UPDATE OF backend ON blobs BEGIN SELECT RAISE(ABORT,'synthetic transaction failure'); END;`); err != nil {
		t.Fatal(err)
	}
	err = a.migrateBlob(ctx, key)
	if err == nil {
		t.Fatal("transaction failure not propagated")
	}
	sameContent(t, h.request("GET", fb.DownloadURL, "", "", userB).Body.Bytes(), payload)
	if _, err = a.db.Exec(`DROP TRIGGER reject_object_switch`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/files/"+fa.ID+"/migrate-s3", nil)
	req.AddCookie(userA)
	req.Header.Set("X-NetDisk-Request", "1")
	req.Header.Set("X-NetDisk-Maintenance", "synthetic-maintenance")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	status(t, w, 200)
	var backend string
	if err = a.db.QueryRow(`SELECT backend FROM blobs WHERE storage_key=?`, key).Scan(&backend); err != nil || backend != "s3" {
		t.Fatal("blob metadata did not switch")
	}
	if _, err = os.Stat(filepath.Join(a.blobs, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("local copy retained after verified migration")
	}
	before := sha256.Sum256(payload)
	w = h.request("GET", fb.DownloadURL, "", "", userB)
	status(t, w, 307)
	if w.Body.Len() != 0 {
		t.Fatal("app proxied object body")
	}
	direct, err := http.Get(w.Header().Get("Location"))
	if err != nil {
		t.Fatal("presigned object request failed")
	}
	body, err := io.ReadAll(direct.Body)
	direct.Body.Close()
	if err != nil || direct.StatusCode != 200 || sha256.Sum256(body) != before {
		t.Fatal("direct object download differs")
	}
	rangeBody, code, headers := httpRange(t, http.DefaultClient, w.Header().Get("Location"), nil, 0, 99)
	if code != 206 || headers.Get("Content-Range") != "bytes 0-99/131072" || !bytes.Equal(rangeBody, payload[:100]) {
		t.Fatal("direct object Range failed")
	}
	_, sharedURL := createShareTest(t, h, userB, "file", fb.ID)
	status(t, h.request("GET", sharedURL, "", "", nil), 307)
	zipped := h.request("GET", "/api/folders/"+folder.ID+"/download", "", "", userB)
	status(t, zipped, 200)
	z, err := zip.NewReader(bytes.NewReader(zipped.Body.Bytes()), int64(zipped.Body.Len()))
	if err != nil || len(z.File) != 1 {
		t.Fatal("object-backed ZIP invalid")
	}
	source, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(source)
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	sameContent(t, b, payload)
	// Restart keeps the migrated metadata and both user file references.
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.app = a
	h.handler = a.Handler()
	status(t, h.request("DELETE", "/api/me", "", "", userA), 204)
	status(t, h.request("GET", fb.DownloadURL, "", "", userB), 307)
	status(t, h.request("DELETE", "/api/files/"+fb.ID, "", "", userB), 204)
	if countQuery(t, a, `SELECT count(*) FROM blobs`) != 0 {
		t.Fatal("last object reference retained")
	}
	_, err = store.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if minio.ToErrorResponse(err).Code != "NoSuchKey" {
		t.Fatal("last object not removed")
	}
	// A cold-content policy candidate executes the same verified migration.
	policyFile := putFile(t, h, userB, "policy-cold.bin", "", randomContent(t, 1<<20))
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	if _, err = a.db.Exec(`UPDATE files SET created_at=? WHERE id=?`, old, policyFile.ID); err != nil {
		t.Fatal(err)
	}
	cs, err := a.candidates(ctx, currentOwner(t, h, userB))
	if err != nil || len(cs) != 1 || cs[0].FileID != policyFile.ID {
		t.Fatal("policy candidate missing", err)
	}
	policyRequest := httptest.NewRequest("POST", "/api/storage/policy", nil)
	policyRequest.AddCookie(userB)
	policyRequest.Header.Set("X-NetDisk-Request", "1")
	policyRequest.Header.Set("X-NetDisk-Maintenance", "synthetic-maintenance")
	policyResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(policyResponse, policyRequest)
	status(t, policyResponse, 200)
	if countQuery(t, a, `SELECT count(*) FROM blobs WHERE backend='s3'`) != 1 {
		t.Fatal("policy did not execute object migration")
	}
	status(t, h.request("GET", policyFile.DownloadURL, "", "", userB), 307)
	status(t, h.request("DELETE", "/api/files/"+policyFile.ID, "", "", userB), 204)
	// No credentials, session cookies, share tokens, or presigned URLs in logs.
	summary, _ := json.Marshal(map[string]bool{"metadata_switch": true, "local_removed": true, "redirect_only": true, "direct_hash": true, "direct_range": true, "object_zip": true, "shared_reference_survived": true, "last_object_removed": true})
	t.Log(string(summary))
}
