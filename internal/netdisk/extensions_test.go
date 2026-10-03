package netdisk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bingyan-netdisk/internal/peer"
	"golang.org/x/crypto/bcrypt"
)

func TestEmailCodeExpirySingleUsePasswordAndSessionInvalidation(t *testing.T) {
	h := newHarness(t, 4096)
	h.app.cfg.DevEmail = true
	c := h.account("email_demo")
	other := h.account("email_other")
	issue := func(email string) string {
		w := h.request("POST", "/api/me/email-code", `{"email":"`+email+`"}`, "application/json", c)
		status(t, w, 201)
		var b struct {
			Code string `json:"dev_code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if len(b.Code) != 6 {
			t.Fatal("invalid generated code")
		}
		return b.Code
	}
	code := issue("demo@example.test")
	var stored string
	h.app.db.QueryRow(`SELECT code_hash FROM email_codes`).Scan(&stored)
	if stored == code || stored != tokenHash(code) {
		t.Fatal("plaintext code stored")
	}
	status(t, h.request("POST", "/api/me/email-code", `{"email":"demo@example.test"}`, "application/json", c), 429)
	binding := `{"email":"demo@example.test","code":"` + code + `"}`
	status(t, h.request("POST", "/api/me/email", binding, "application/json", other), 400)
	status(t, h.request("POST", "/api/me/email", `{"email":"wrong@example.test","code":"`+code+`"}`, "application/json", c), 400)
	status(t, h.request("POST", "/api/me/email", binding, "application/json", c), 200)
	status(t, h.request("POST", "/api/me/email", binding, "application/json", c), 400)
	code = issue("expired@example.test")
	h.app.db.Exec(`UPDATE email_codes SET expires_at=0`)
	status(t, h.request("POST", "/api/me/email", `{"email":"expired@example.test","code":"`+code+`"}`, "application/json", c), 400)
	h.app.db.Exec(`DELETE FROM email_codes`)
	code = issue("attempts@example.test")
	for i := 0; i < 5; i++ {
		status(t, h.request("POST", "/api/me/email", `{"email":"attempts@example.test","code":"abcdef"}`, "application/json", c), 400)
	}
	status(t, h.request("POST", "/api/me/email", `{"email":"attempts@example.test","code":"`+code+`"}`, "application/json", c), 400)
	var exported struct {
		ID string `json:"id"`
	}
	w := h.request("POST", "/api/nfs/exports", "", "", c)
	status(t, w, 201)
	json.Unmarshal(w.Body.Bytes(), &exported)
	status(t, h.request("POST", "/api/me/password", `{"old_password":"incorrect","new_password":"new-password-123"}`, "application/json", c), 403)
	status(t, h.request("POST", "/api/me/password", `{"old_password":"temporary-test-password","new_password":"new-password-123"}`, "application/json", c), 204)
	status(t, h.request("GET", "/api/me", "", "", c), 401)
	if countQuery(t, h.app, `SELECT count(*) FROM nfs_exports`) != 0 {
		t.Fatal("password change left exports alive")
	}
	status(t, h.request("POST", "/api/login", `{"username":"email_demo","password":"temporary-test-password"}`, "application/json", nil), 401)
	status(t, h.request("POST", "/api/login", `{"username":"email_demo","password":"new-password-123"}`, "application/json", nil), 200)
	var passwordHash string
	h.app.db.QueryRow(`SELECT password_hash FROM users WHERE username='email_demo'`).Scan(&passwordHash)
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("new-password-123")) != nil {
		t.Fatal("password not bcrypt")
	}
}
func TestStatsQuotaReservationsDedupAndRecovery(t *testing.T) {
	h := newHarness(t, 4096)
	h.app.cfg.QuotaBytes = 10
	c := h.account("quota_demo")
	other := h.account("quota_other")
	f := putFile(t, h, c, "first", "", []byte("123456"))
	putFile(t, h, other, "same", "", []byte("123456"))
	status(t, h.reader("POST", "/api/files?name=second", strings.NewReader("12345"), "application/octet-stream", c), 413)
	s := newSession(t, h, c, "reserved", "", 4, 2)
	status(t, h.reader("POST", "/api/files?name=blocked", strings.NewReader("1"), "application/octet-stream", c), 413)
	status(t, h.request("POST", "/api/uploads", `{"name":"too-much","expected_size":1,"part_size":1}`, "application/json", c), 413)
	part(t, h, c, s.ID, 0, []byte("12"), 201)
	part(t, h, c, s.ID, 1, []byte("34"), 201)
	status(t, h.request("POST", "/api/uploads/"+s.ID+"/complete", "", "", c), 201)
	stats := func() (used, reserved, files int64) {
		w := h.request("GET", "/api/me/stats", "", "", c)
		status(t, w, 200)
		var b map[string]int64 // ignore the separate boolean adapter flag
		var raw map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &raw)
		delete(raw, "dev_email")
		bb, _ := json.Marshal(raw)
		json.Unmarshal(bb, &b)
		return b["used_bytes"], b["reserved_bytes"], b["files"]
	}
	if used, reserved, files := stats(); used != 10 || reserved != 0 || files != 2 {
		t.Fatal("incorrect stats or reservations")
	}
	status(t, h.request("DELETE", "/api/files/"+f.ID, "", "", c), 204)
	if used, _, _ := stats(); used != 4 {
		t.Fatal("quota not recovered")
	}
	putFile(t, h, c, "same-content", "", []byte("123456"))
	if countQuery(t, h.app, `SELECT count(*) FROM blobs`) != 2 {
		t.Fatal("quota upload bypassed dedupe")
	}
}
func TestShareAnalyticsOwnershipRetentionAndPolicyCandidates(t *testing.T) {
	h := newHarness(t, 2<<20)
	c := h.account("analytics")
	other := h.account("analytics_other")
	root := makeFolder(t, h, c, "root", "")
	f := putFile(t, h, c, "cold", root.ID, randomContent(t, 1<<20))
	putFile(t, h, other, "other-cold", "", randomContent(t, 1<<20))
	share, url := createShareTest(t, h, c, "folder", root.ID)
	status(t, h.request("GET", url, "", "", nil), 200)
	status(t, h.request("GET", url+"/files/"+f.ID+"/download", "", "", nil), 200)
	status(t, h.request("GET", "/api/shares/"+share.ID+"/analytics", "", "", other), 404)
	w := h.request("GET", "/api/shares/"+share.ID+"/analytics", "", "", c)
	status(t, w, 200)
	var b struct {
		Views     int `json:"views"`
		Downloads int `json:"download_requests"`
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	if b.Views != 1 || b.Downloads != 1 {
		t.Fatal("analytics counts incorrect")
	}
	if strings.Contains(w.Body.String(), strings.TrimPrefix(url, "/s/")) {
		t.Fatal("analytics leaked capability")
	}
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	h.app.db.Exec(`UPDATE files SET created_at=?`, old)
	h.app.db.Exec(`UPDATE blobs SET last_access=?`, old)
	cs, err := h.app.candidates(context.Background(), currentOwner(t, h, c))
	if err != nil || len(cs) != 1 || cs[0].FileID != f.ID {
		t.Fatal("policy selection/isolation incorrect", err)
	}
	h.app.db.Exec(`UPDATE blobs SET access_count=3 WHERE id=(SELECT blob_id FROM files WHERE id=?)`, f.ID)
	cs, err = h.app.candidates(context.Background(), currentOwner(t, h, c))
	if err != nil || len(cs) != 0 {
		t.Fatal("hot blob selected")
	}
	status(t, h.request("POST", "/api/storage/policy", "", "", c), 403)
	h.app.db.Exec(`UPDATE share_events SET created_at=?`, time.Now().Add(-31*24*time.Hour).Unix())
	if err = h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, `SELECT count(*) FROM share_events`) != 0 {
		t.Fatal("old events not cleaned")
	}
}
func currentOwner(t *testing.T, h *harness, c *http.Cookie) int64 {
	t.Helper()
	var u User
	json.Unmarshal(h.request("GET", "/api/me", "", "", c).Body.Bytes(), &u)
	return u.ID
}
func TestP2PTwoClientsDirectTLSHashIsolationRevocation(t *testing.T) {
	h := newHarness(t, 2<<20)
	c := h.account("peer_sender")
	other := h.account("peer_other")
	payload := randomContent(t, 1<<20)
	f := putFile(t, h, c, "peer.bin", "", payload)
	share, link := createShareTest(t, h, c, "p2p", f.ID)
	token := strings.TrimPrefix(link, "/s/")
	source := filepath.Join(t.TempDir(), "source")
	os.WriteFile(source, payload, 0600)
	sender, err := peer.NewSender("127.0.0.1:0", source, token)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	sender.Authorize = func(ctx context.Context) error {
		w := h.request("GET", link, "", "", nil)
		if w.Code != 200 {
			return os.ErrPermission
		}
		return nil
	}
	offer, _ := json.Marshal(map[string]any{"address": sender.Info.Address, "size": sender.Info.Size, "sha256": sender.Info.Hash, "fingerprint": sender.Info.Fingerprint})
	status(t, h.request("POST", "/api/p2p/"+share.ID+"/offer", string(offer), "application/json", other), 404)
	status(t, h.request("POST", "/api/p2p/"+share.ID+"/offer", string(offer), "application/json", c), 200)
	status(t, h.request("GET", link+"/files/"+f.ID+"/download", "", "", nil), 404)
	var appResponseBytes int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, r)
		appResponseBytes += int64(rec.Body.Len())
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	defer server.Close()
	res, err := http.Get(server.URL + link)
	if err != nil {
		t.Fatal(err)
	}
	var info peer.Info
	err = json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- sender.ServeOne(ctx) }()
	dest := filepath.Join(t.TempDir(), "received")
	if err = peer.Receive(ctx, info, token, dest); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	sameContent(t, got, payload)
	if appResponseBytes >= 4096 || appResponseBytes == 0 {
		t.Fatal("application proxied peer body")
	}
	t.Logf("direct TLS peers: %d bytes; signaling response: %d bytes; hash matched", len(got), appResponseBytes)
	status(t, h.request("DELETE", "/api/shares/"+share.ID, "", "", c), 204)
	status(t, h.request("GET", link, "", "", nil), 404)
	// Even previously fetched signaling cannot bypass revocation in the sender.
	revokedSender, err := peer.NewSender("127.0.0.1:0", source, token)
	if err != nil {
		t.Fatal(err)
	}
	defer revokedSender.Close()
	revokedSender.Authorize = sender.Authorize
	revokedDone := make(chan error, 1)
	go func() { revokedDone <- revokedSender.ServeOne(ctx) }()
	if err = peer.Receive(ctx, revokedSender.Info, token, filepath.Join(t.TempDir(), "revoked")); err == nil {
		t.Fatal("revoked peer capability transferred content")
	}
	if err = <-revokedDone; err == nil {
		t.Fatal("sender ignored revoked signal")
	}
	// Expired offers and deleted source invalidate signaling.
	share, link = createShareTest(t, h, c, "p2p", f.ID)
	status(t, h.request("POST", "/api/p2p/"+share.ID+"/offer", string(offer), "application/json", c), 200)
	h.app.db.Exec(`UPDATE p2p_offers SET expires_at=0`)
	status(t, h.request("GET", link, "", "", nil), 404)
	status(t, h.request("DELETE", "/api/files/"+f.ID, "", "", c), 204)
	status(t, h.request("GET", link, "", "", nil), 404)
}
func TestP2PRejectsCorruptHashAndWrongTLSIdentity(t *testing.T) {
	payload := []byte("synthetic peer data")
	source := filepath.Join(t.TempDir(), "src")
	os.WriteFile(source, payload, 0600)
	token := strings.Repeat("a", 64)
	for _, bad := range []string{"hash", "fingerprint", "token"} {
		t.Run(bad, func(t *testing.T) {
			sender, err := peer.NewSender("127.0.0.1:0", source, token)
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			info := sender.Info
			provided := token
			if bad == "hash" {
				info.Hash = strings.Repeat("0", 64)
			}
			if bad == "fingerprint" {
				info.Fingerprint = strings.Repeat("0", 64)
			}
			if bad == "token" {
				provided = strings.Repeat("b", 64)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- sender.ServeOne(ctx) }()
			dest := filepath.Join(t.TempDir(), "new-file")
			if err = peer.Receive(ctx, info, provided, dest); err == nil {
				t.Fatal("corrupt transfer accepted")
			}
			if _, err = os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("bad content published")
			}
			<-done
		})
	}
}
