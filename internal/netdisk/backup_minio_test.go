package netdisk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestPullMinIODownloadArchiveAndRestore(t *testing.T) {
	endpoint := os.Getenv("NETDISK_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("NOT_RUN: isolated local MinIO endpoint required")
	}
	h := newHarness(t, 1024)
	upstream, err := url.Parse("http://" + endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var ranges atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-NetDisk-Request") != "" {
			t.Error("API credentials leaked to object download")
		}
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	suffix, _ := randomKey(8)
	bucket := "netdisk-pull-test-" + suffix
	store, err := newObjectStore(S3Config{Endpoint: endpoint, DownloadEndpoint: strings.TrimPrefix(gateway.URL, "http://"), Bucket: bucket, AccessKey: "netdisk-test-only", SecretKey: "netdisk-test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	h.app.store = store
	ctx := context.Background()
	if err = store.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatal("isolated bucket unavailable")
	}
	defer func() {
		for obj := range store.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err == nil {
				_ = store.remove(ctx, obj.Key)
			}
		}
		_ = store.client.RemoveBucket(ctx, bucket)
	}()
	c, cfg, cookie := pullFixture(t, h, h.handler, "pull_minio")
	f := pullUpload(t, h, cookie, "", "object.txt", "object backup bytes")
	var key string
	if err = h.app.db.QueryRow("SELECT storage_key FROM blobs WHERE size=19").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err = h.app.migrateBlob(ctx, key); err != nil {
		t.Fatal(err)
	}
	e := pullEngine(t, cfg, c)
	defer e.Close()
	r := pullOnce(t, e)
	if r.Downloaded != 1 || ranges.Load() != 5 {
		t.Fatal("S3 ranges were not used")
	}
	if _, err = e.Archive(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{}); err == nil {
		t.Fatal("unreferenced archived object retained")
	}
	if _, err = e.Restore(ctx, f.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestBackupMinIOReconcileRedirectCookieIsolation(t *testing.T) {
	endpoint := os.Getenv("NETDISK_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("NOT_RUN: isolated local MinIO endpoint required")
	}
	h := newHarness(t, 1024)
	upstream, err := url.Parse("http://" + endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var seen atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(cookieName); err == nil {
			t.Error("session cookie leaked to object endpoint")
		}
		seen.Store(true)
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	suffix, err := randomKey(8)
	if err != nil {
		t.Fatal(err)
	}
	bucket := "netdisk-backup-test-" + suffix
	store, err := newObjectStore(S3Config{Endpoint: endpoint, DownloadEndpoint: strings.TrimPrefix(gateway.URL, "http://"), Bucket: bucket, AccessKey: "netdisk-test-only", SecretKey: "netdisk-test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	h.app.store = store
	ctx := context.Background()
	if err = store.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatal("isolated bucket unavailable")
	}
	defer func() {
		for obj := range store.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err == nil {
				_ = store.remove(ctx, obj.Key)
			}
		}
		_ = store.client.RemoveBucket(ctx, bucket)
	}()
	client, cfg := backupFixture(t, h, h.handler, "backup_minio")
	backupWrite(t, cfg.Root, "object.txt", "object backup bytes")
	e := backupEngine(t, cfg, client)
	if r := backupCycle(t, e); r.Uploaded != 1 {
		t.Fatalf("initial object fixture: %+v", r)
	}
	e.Close()
	var key string
	if err = h.app.db.QueryRow("SELECT storage_key FROM blobs WHERE size=19").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err = h.app.migrateBlob(ctx, key); err != nil {
		t.Fatal(err)
	}
	cfg.StatePath = filepath.Join(t.TempDir(), "recovery.json")
	e = backupEngine(t, cfg, client)
	defer e.Close()
	if r := backupCycle(t, e); r.Uploaded != 1 || backupRows(t, h) != 1 {
		t.Fatalf("object recovery duplicated: %+v", r)
	}
	if !seen.Load() {
		t.Fatal("real object gateway was not used")
	}
	t.Log("real MinIO migrated version reconciled by streamed hash; same-host/different-port object gateway received no NetDisk session cookie PASS")
}
