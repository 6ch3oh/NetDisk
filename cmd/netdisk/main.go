package main

import (
	"context"
	"errors"
	"log"
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
	app, err := netdisk.New(netdisk.Config{
		DataDir:        env("NETDISK_DATA_DIR", "./data"),
		Origin:         env("NETDISK_ORIGIN", "http://127.0.0.1:38120"),
		MaxUploadBytes: limit, CookieSecure: secure,
	})
	if err != nil {
		log.Fatal("could not initialize storage: ", err)
	}
	defer app.Close()
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
