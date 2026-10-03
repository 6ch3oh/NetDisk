package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"bingyan-netdisk/internal/netdisk"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	limit, err := strconv.ParseInt(env("NETDISK_MAX_UPLOAD_BYTES", "104857600"), 10, 64)
	if err != nil || limit <= 0 {
		log.Fatal("invalid upload limit")
	}
	secure, err := strconv.ParseBool(env("NETDISK_COOKIE_SECURE", "false"))
	if err != nil {
		log.Fatal("invalid cookie security setting")
	}
	var s3 *netdisk.S3Config
	if endpoint := os.Getenv("NETDISK_S3_ENDPOINT"); endpoint != "" {
		tls, e := strconv.ParseBool(env("NETDISK_S3_SECURE", "false"))
		if e != nil {
			log.Fatal("invalid object storage TLS setting")
		}
		s3 = &netdisk.S3Config{Endpoint: endpoint, DownloadEndpoint: os.Getenv("NETDISK_S3_DOWNLOAD_ENDPOINT"), Bucket: os.Getenv("NETDISK_S3_BUCKET"), AccessKey: os.Getenv("NETDISK_S3_ACCESS_KEY"), SecretKey: os.Getenv("NETDISK_S3_SECRET_KEY"), Secure: tls}
	}
	quota, err := strconv.ParseInt(env("NETDISK_QUOTA_BYTES", "1073741824"), 10, 64)
	if err != nil || quota <= 0 {
		log.Fatal("invalid quota")
	}
	devEmail, err := strconv.ParseBool(env("NETDISK_DEV_EMAIL", "false"))
	if err != nil {
		log.Fatal("invalid email mode")
	}
	app, err := netdisk.New(netdisk.Config{
		DataDir:        env("NETDISK_DATA_DIR", "./data"),
		Origin:         env("NETDISK_ORIGIN", "http://127.0.0.1:38120"),
		MaxUploadBytes: limit, CookieSecure: secure, S3: s3, MaintenanceKey: os.Getenv("NETDISK_MAINTENANCE_KEY"), QuotaBytes: quota, DevEmail: devEmail,
	})
	if err != nil {
		log.Fatal("could not initialize storage: ", err)
	}
	defer app.Close()
	if addr := os.Getenv("NETDISK_NFS_ADDR"); addr != "" {
		host, _, e := net.SplitHostPort(addr)
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
			log.Fatal("NFS listener requires a local/private IP; publish Docker ports on loopback only")
		}
		listener, e := net.Listen("tcp", addr)
		if e != nil {
			log.Fatal("NFS listener unavailable")
		}
		defer listener.Close()
		go func() {
			if e := app.ServeNFS(listener); e != nil && !errors.Is(e, net.ErrClosed) {
				log.Print("NFS listener stopped")
			}
		}()
	}
	server := &http.Server{Addr: env("NETDISK_ADDR", "127.0.0.1:38120"), Handler: app.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Minute,
		WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { log.Print("NetDisk listening on ", server.Addr); done <- server.ListenAndServe() }()
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("server stopped: ", err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}
}
