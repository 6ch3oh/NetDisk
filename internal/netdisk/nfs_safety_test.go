package netdisk

import (
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"
)

func TestNFSReplacementQuotaAttributesChrootAndAccountDeletion(t *testing.T) {
	h := newHarness(t, 4096)
	c := h.account("nfs_safety")
	other := h.account("nfs_survivor")
	source := []byte("shared original")
	mine := putFile(t, h, c, "shared", "", source)
	survivor := putFile(t, h, other, "shared", "", source)
	var export struct {
		ID string `json:"id"`
	}
	json.Unmarshal(h.request("POST", "/api/nfs/exports", "", "", c).Body.Bytes(), &export)
	fs := &logicalFS{a: h.app, owner: currentOwner(t, h, c), export: export.ID}
	file, err := fs.Create("shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	sameContent(t, h.request("GET", survivor.DownloadURL, "", "", other).Body.Bytes(), source)
	sameContent(t, h.request("GET", mine.DownloadURL, "", "", c).Body.Bytes(), []byte("changed"))
	if countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 2 {
		t.Fatal("replacement damaged references")
	}
	if err = fs.Chmod("shared", 0640); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1234567890, 0)
	if err = fs.Chtimes("shared", mt, mt); err != nil {
		t.Fatal(err)
	}
	stat, err := fs.Stat("shared")
	if err != nil || stat.Mode().Perm() != 0640 || !stat.ModTime().Equal(mt) {
		t.Fatal("logical attributes not persisted", err)
	}
	if err = fs.Chown("shared", 42, 42); err == nil {
		t.Fatal("client chose app ownership")
	}
	h.app.cfg.QuotaBytes = 7
	file, err = fs.Create("shared")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("too large"))
	if err = file.Close(); err == nil {
		t.Fatal("NFS bypassed quota")
	}
	sameContent(t, h.request("GET", mine.DownloadURL, "", "", c).Body.Bytes(), []byte("changed"))
	if entries(t, h.app.temp) != 0 {
		t.Fatal("failed replacement left scratch content")
	}
	if err = fs.MkdirAll("root/child", 0700); err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Chroot("root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sub.Stat("../shared"); err == nil {
		t.Fatal("chroot escaped")
	}
	if err = fs.Rename("root", "root/child/cycle"); err == nil {
		t.Fatal("NFS created a cycle")
	}
	file, err = fs.OpenFile("shared", os.O_RDONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	status(t, h.request("DELETE", "/api/me", "", "", c), 204)
	if _, err = io.ReadAll(file); err == nil {
		t.Fatal("deleted account open descriptor still readable")
	}
	file.Close()
	sameContent(t, h.request("GET", survivor.DownloadURL, "", "", other).Body.Bytes(), source)
	if countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 1 {
		t.Fatal("account deletion removed another user blob")
	}
}
