package netdisk

import (
	"net"
	"net/http"
	"strconv"
	"time"
)

func (a *App) p2pRoutes(m *http.ServeMux) {
	m.Handle("POST /api/p2p/{id}/offer", a.auth(http.HandlerFunc(a.offerP2P)))
}
func (a *App) offerP2P(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address     string `json:"address"`
		Hash        string `json:"sha256"`
		Size        int64  `json:"size"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &b) {
		return
	}
	host, port, err := net.SplitHostPort(b.Address)
	p, e := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || e != nil || p < 1 || p > 65535 || ip == nil || !(ip.IsLoopback() || ip.IsPrivate()) || ip.IsUnspecified() {
		fail(w, 400, "P2P requires a LAN or loopback IP and port")
		return
	}
	var hash string
	var size int64
	err = a.db.QueryRowContext(r.Context(), `SELECT b.content_hash,b.size FROM shares s JOIN files f ON s.file_id=f.id JOIN blobs b ON f.blob_id=b.id WHERE s.id=? AND s.user_id=? AND f.user_id=s.user_id AND f.deleted=0 AND s.share_type='p2p'`, r.PathValue("id"), current(r).user.ID).Scan(&hash, &size)
	if err != nil {
		fileError(w, err)
		return
	}
	if b.Hash != hash || b.Size != size || len(b.Fingerprint) != 64 {
		fail(w, 409, "local source must match shared file and provide TLS fingerprint")
		return
	}
	expires := time.Now().Add(10 * time.Minute).Unix()
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO p2p_offers(share_id,address,expires_at,fingerprint) VALUES(?,?,?,?) ON CONFLICT(share_id) DO UPDATE SET address=excluded.address,expires_at=excluded.expires_at,fingerprint=excluded.fingerprint`, r.PathValue("id"), b.Address, expires, b.Fingerprint)
	if err != nil {
		fileError(w, err)
		return
	}
	respond(w, 200, map[string]int64{"expires_at": expires})
}
func (a *App) signalP2P(w http.ResponseWriter, r *http.Request, s Share, owner int64) {
	var address, hash, name, fp string
	var size, expires int64
	err := a.db.QueryRowContext(r.Context(), `SELECT p.address,p.expires_at,f.name,b.size,b.content_hash,p.fingerprint FROM p2p_offers p JOIN shares s ON p.share_id=s.id JOIN files f ON s.file_id=f.id JOIN blobs b ON f.blob_id=b.id WHERE s.id=? AND s.user_id=? AND p.expires_at>? AND f.deleted=0`, s.ID, owner, time.Now().Unix()).Scan(&address, &expires, &name, &size, &hash, &fp)
	if err != nil {
		fileError(w, err)
		return
	}
	if r.Method == "GET" {
		if err = a.recordShare(r.Context(), s.ID, "signal"); err != nil {
			fileError(w, err)
			return
		}
	}
	respond(w, 200, map[string]any{"type": "p2p", "address": address, "expires_at": expires, "name": name, "size": size, "sha256": hash, "fingerprint": fp})
}
