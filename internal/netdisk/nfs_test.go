package netdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nfsclient "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestNFSRealProtocolLogicalReadWriteDirectoriesIsolationRevocation(t *testing.T) {
	h := newHarness(t, 2<<20)
	c := h.account("nfs_demo")
	other := h.account("nfs_other")
	payload := randomContent(t, 64<<10)
	putFile(t, h, c, "source.bin", "", payload)
	putFile(t, h, other, "private.bin", "", []byte("other user"))
	w := h.request("POST", "/api/nfs/exports", "", "", c)
	status(t, w, 201)
	var exported struct {
		ID   string `json:"id"`
		Path string `json:"mount_path"`
	}
	json.Unmarshal(w.Body.Bytes(), &exported)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go h.app.ServeNFS(listener)
	client, err := rpc.DialTCP("tcp", listener.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	mount := &nfsclient.Mount{Client: client}
	if _, err = mount.Mount("/"+strings.Repeat("0", 64), rpc.AuthNull); err == nil {
		t.Fatal("invalid export mounted")
	}
	target, err := mount.Mount(exported.Path, rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	file, err := target.Open("source.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	sameContent(t, got, payload)
	if _, _, err = target.Lookup("private.bin"); err == nil {
		t.Fatal("cross-user NFS read")
	}
	if _, err = target.Mkdir("nested", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Mkdir("nested/child", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Create("nested/child/new.bin", 0600); err != nil {
		t.Fatal(err)
	}
	file, err = target.OpenFile("nested/child/new.bin", 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	file, err = target.Open("nested/child/new.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	sameContent(t, got, payload)
	if countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 2 {
		t.Fatal("NFS write bypassed dedupe")
	}
	if err = target.Rename("nested/child/new.bin", "nested/renamed.bin"); err != nil {
		t.Fatal(err)
	}
	entries, err := target.ReadDirPlus("nested")
	if err != nil || len(entries) != 2 {
		t.Fatal("protocol directory listing", err)
	}
	if err = target.Remove("nested/renamed.bin"); err != nil {
		t.Fatal(err)
	}
	if err = target.RmDir("nested/child"); err != nil {
		t.Fatal(err)
	}
	if err = target.RmDir("nested"); err != nil {
		t.Fatal(err)
	}
	// Path normalization and symlinks cannot expose host storage.
	fs := &logicalFS{a: h.app, owner: currentOwner(t, h, c), export: exported.ID}
	for _, bad := range []string{"../blobs", "a/../../outside", `C:\data`, "a\x00b"} {
		if _, err = fs.Stat(bad); err == nil {
			t.Fatal("unsafe logical path accepted")
		}
	}
	if err = fs.Symlink("/data/blobs", "link"); err == nil {
		t.Fatal("symlink allowed")
	}
	status(t, h.request("DELETE", "/api/nfs/exports/"+exported.ID, "", "", other), 404)
	status(t, h.request("DELETE", "/api/nfs/exports/"+exported.ID, "", "", c), 204)
	if _, _, err = target.Lookup("source.bin"); err == nil {
		t.Fatal("mounted capability remained usable after revoke")
	}
	t.Log("real Linux TCP NFSv3 MOUNT/READ/WRITE/READDIR/MKDIR/RENAME/REMOVE/RMDIR PASS; logical metadata and dedupe verified")
}
func TestNFSKernelMount(t *testing.T) {
	if os.Getenv("NETDISK_TEST_NFS_MOUNT") != "1" {
		t.Skip("NOT_RUN: opt-in isolated Linux kernel mount")
	}
	h := newHarness(t, 2<<20)
	c := h.account("nfs_mount")
	payload := []byte("synthetic kernel NFS mount content")
	putFile(t, h, c, "web-source.txt", "", payload)
	var e struct {
		Path string `json:"mount_path"`
	}
	json.Unmarshal(h.request("POST", "/api/nfs/exports", "", "", c).Body.Bytes(), &e)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go h.app.ServeNFS(listener)
	port := listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", "vers=3,tcp,port="+jsonNumber(port)+",mountport="+jsonNumber(port)+",nolock,soft,timeo=10,retrans=1", "127.0.0.1:"+e.Path, dir)
	// Never print command arguments: the mount path is a capability.
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.ReplaceAll(string(output), e.Path, "/[REDACTED]")
		t.Fatalf("BLOCKED: isolated Linux kernel NFS mount: %v %s", err, message)
	}
	defer exec.Command("umount", dir).Run()
	got, err := os.ReadFile(filepath.Join(dir, "web-source.txt"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("kernel read differs", err)
	}
	if err = os.Mkdir(filepath.Join(dir, "new-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "new-dir", "created.txt")
	if err = os.WriteFile(target, payload, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("kernel write differs", err)
	}
	if err = os.Rename(target, filepath.Join(dir, "new-dir", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "new-dir", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "new-dir")); err != nil {
		t.Fatal(err)
	}
	t.Log("kernel mount/read/write/mkdir/rename/remove/rmdir PASS")
}
func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }
