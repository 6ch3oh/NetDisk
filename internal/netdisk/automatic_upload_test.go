package netdisk

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func keyedSession(t *testing.T, h *harness, cookie *http.Cookie, key, name string, want int) UploadSession {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": name, "folder_id": "", "expected_size": 8, "part_size": 4, "request_key": key})
	if err != nil {
		t.Fatal(err)
	}
	w := h.request("POST", "/api/uploads", string(body), "application/json", cookie)
	status(t, w, want)
	var session UploadSession
	if want == 201 {
		if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
	}
	return session
}

func TestAutomaticUploadRequestIdempotenceRestartOwnershipAndReceipt(t *testing.T) {
	h := newHarness(t, 1024)
	a, b := h.account("auto_a"), h.account("auto_b")
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s := keyedSession(t, h, a, key, "auto.bin", 201)
	retry := keyedSession(t, h, a, key, "auto.bin", 201)
	if retry.ID != s.ID || countQuery(t, h.app, `SELECT count(*) FROM upload_sessions`) != 1 {
		t.Fatal("creation retry duplicated reservation")
	}
	keyedSession(t, h, a, key, "different.bin", 409)
	keyedSession(t, h, a, "invalid", "auto.bin", 400)
	other := keyedSession(t, h, b, key, "auto.bin", 201)
	if other.ID == s.ID {
		t.Fatal("request key crosses user boundary")
	}
	base := "/api/uploads/" + s.ID
	part(t, h, a, s.ID, 0, []byte("abcd"), 201)
	restartHarness(t, h)
	if keyedSession(t, h, a, key, "auto.bin", 201).ID != s.ID {
		t.Fatal("request lost on restart")
	}
	part(t, h, a, s.ID, 1, []byte("efgh"), 201)
	w := h.request("POST", base+"/complete", "", "", a)
	status(t, w, 201)
	f := parseFile(t, w)
	restartHarness(t, h)
	w = h.request("POST", base+"/complete", "", "", a)
	status(t, w, 201)
	if parseFile(t, w).ID != f.ID {
		t.Fatal("completion retry created another file")
	}
	retry = keyedSession(t, h, a, key, "auto.bin", 201)
	if retry.File == nil || retry.File.ID != f.ID {
		t.Fatal("missing durable receipt")
	}
	status(t, h.request("POST", base+"/complete", "", "", b), 404)
	status(t, h.request("GET", base, "", "", a), 404)
	if countQuery(t, h.app, `SELECT count(*) FROM files`) != 1 || countQuery(t, h.app, `SELECT ref_count FROM blobs`) != 1 {
		t.Fatal("retry corrupted file/ref counts")
	}
	sameContent(t, h.request("GET", f.DownloadURL, "", "", a).Body.Bytes(), []byte("abcdefgh"))
	status(t, h.request("DELETE", "/api/files/"+f.ID, "", "", a), 204)
	status(t, h.request("POST", base+"/complete", "", "", a), 404)
	if keyedSession(t, h, a, key, "auto.bin", 201).ID == s.ID {
		t.Fatal("deleted file retains stale receipt")
	}
}

func TestAutomaticUploadRequestCancelExpiryAndAccountCleanup(t *testing.T) {
	h := newHarness(t, 1024)
	c := h.account("auto_cleanup")
	key := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s := keyedSession(t, h, c, key, "cancel.bin", 201)
	part(t, h, c, s.ID, 0, []byte("abcd"), 201)
	status(t, h.request("DELETE", "/api/uploads/"+s.ID, "", "", c), 204)
	if countQuery(t, h.app, `SELECT count(*) FROM upload_requests`) != 0 {
		t.Fatal("cancellation leaked request")
	}
	s = keyedSession(t, h, c, key, "expired.bin", 201)
	part(t, h, c, s.ID, 0, []byte("abcd"), 201)
	part(t, h, c, s.ID, 1, []byte("efgh"), 201)
	status(t, h.request("POST", "/api/uploads/"+s.ID+"/complete", "", "", c), 201)
	if _, err := h.app.db.Exec(`UPDATE upload_requests SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if err := h.app.cleanupLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if countQuery(t, h.app, `SELECT count(*) FROM upload_requests`) != 0 || countQuery(t, h.app, `SELECT count(*) FROM files`) != 1 {
		t.Fatal("expiry must drop receipt while retaining file")
	}
	keyedSession(t, h, c, key, "account.bin", 201)
	status(t, h.request("DELETE", "/api/me", "", "", c), 204)
	if countQuery(t, h.app, `SELECT count(*) FROM upload_requests`) != 0 {
		t.Fatal("account removal leaked requests")
	}
}
