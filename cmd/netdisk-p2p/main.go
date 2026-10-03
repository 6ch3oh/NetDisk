package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"bingyan-netdisk/internal/peer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "P2P failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("use send or receive")
	}
	f := flag.NewFlagSet("p2p", flag.ContinueOnError)
	server := f.String("server", "http://127.0.0.1:38120", "signaling origin")
	fileID := f.String("file-id", "", "owned netdisk file ID")
	source := f.String("source", "", "local matching file")
	listen := f.String("listen", "127.0.0.1:0", "private LAN IP:port")
	linkFile := f.String("link-file", "", "private file to save/read capability link")
	dest := f.String("dest", "", "new destination path")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *linkFile == "" {
		return errors.New("--link-file required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	if os.Args[1] == "send" {
		cookie := os.Getenv("NETDISK_SESSION")
		if cookie == "" {
			return errors.New("NETDISK_SESSION required")
		}
		var created struct {
			Share struct {
				ID string `json:"id"`
			} `json:"share"`
			URL string `json:"url"`
		}
		if err := api(ctx, client, "POST", *server+"/api/shares", cookie, map[string]string{"resource_type": "p2p", "resource_id": *fileID}, &created); err != nil {
			return err
		}
		token := strings.TrimPrefix(created.URL, "/s/")
		sender, err := peer.NewSender(*listen, *source, token)
		if err != nil {
			return err
		}
		defer sender.Close()
		if err = api(ctx, client, "POST", *server+"/api/p2p/"+created.Share.ID+"/offer", cookie, map[string]any{"address": sender.Info.Address, "size": sender.Info.Size, "sha256": sender.Info.Hash, "fingerprint": sender.Info.Fingerprint}, nil); err != nil {
			return err
		}
		link, err := url.Parse(*server)
		if err != nil {
			return err
		}
		link.Path = created.URL
		sender.Authorize = func(ctx context.Context) error {
			var current peer.Info
			if err := api(ctx, client, "GET", link.String(), "", nil, &current); err != nil {
				return err
			}
			if current.Fingerprint != sender.Info.Fingerprint || current.Hash != sender.Info.Hash {
				return errors.New("peer signal changed")
			}
			return nil
		}
		file, err := os.OpenFile(*linkFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.WriteString(file, link.String())
		file.Close()
		if err != nil {
			return err
		}
		fmt.Println("Capability saved to the private link file; waiting for a direct peer.")
		if err = sender.ServeOne(ctx); err != nil {
			return err
		}
		fmt.Println("Direct transfer complete.")
		return nil
	}
	if os.Args[1] != "receive" {
		return errors.New("use send or receive")
	}
	b, err := os.ReadFile(*linkFile)
	if err != nil {
		return err
	}
	link, err := url.Parse(strings.TrimSpace(string(b)))
	if err != nil || link == nil || !strings.HasPrefix(link.Path, "/s/") {
		return errors.New("invalid link file")
	}
	token := strings.TrimPrefix(link.Path, "/s/")
	var info peer.Info
	if err = api(ctx, client, "GET", link.String(), "", nil, &info); err != nil {
		return err
	}
	if err = peer.Receive(ctx, info, token, *dest); err != nil {
		return err
	}
	fmt.Printf("Direct transfer verified: %d bytes, SHA-256 %s\n", info.Size, info.Hash)
	return nil
}
func api(ctx context.Context, c *http.Client, method, endpoint, cookie string, body, out any) error {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "netdisk_session", Value: cookie})
	}
	req.Header.Set("X-NetDisk-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return errors.New("signaling connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("signaling HTTP %d", res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(out)
	}
	return nil
}
