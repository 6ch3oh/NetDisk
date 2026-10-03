// Package peer implements the demonstration's direct, TLS-encrypted LAN stream.
// The signaling application receives only metadata, never this file body.
package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Info struct {
	Address     string `json:"address"`
	Size        int64  `json:"size"`
	Hash        string `json:"sha256"`
	Fingerprint string `json:"fingerprint"`
	Expires     int64  `json:"expires_at"`
}
type Sender struct {
	Listener          net.Listener
	Info              Info
	source, tokenHash string
	Authorize         func(context.Context) error
}

func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func privateAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && !ip.IsUnspecified() && (ip.IsPrivate() || ip.IsLoopback())
}
func NewSender(address, source, token string) (*Sender, error) {
	if !privateAddress(address) || len(token) != 64 {
		return nil, errors.New("invalid private peer address or capability")
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	size, err := io.CopyBuffer(h, file, make([]byte, 32768))
	file.Close()
	if err != nil {
		return nil, err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "NetDisk ephemeral LAN peer"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(15 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		return nil, err
	}
	listener, err := tls.Listen("tcp", address, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		return nil, err
	}
	fp := sha256.Sum256(der)
	return &Sender{Listener: listener, Info: Info{listener.Addr().String(), size, hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(fp[:]), time.Now().Add(10 * time.Minute).Unix()}, source: source, tokenHash: digest(token)}, nil
}
func (s *Sender) Close() error { return s.Listener.Close() }
func (s *Sender) ServeOne(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { s.Listener.Close() })
	defer stop()
	conn, err := s.Listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	handshake := make([]byte, 64)
	if _, err = io.ReadFull(conn, handshake); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(digest(string(handshake))), []byte(s.tokenHash)) != 1 || time.Now().Unix() >= s.Info.Expires {
		return errors.New("invalid peer capability")
	}
	file, err := os.Open(s.source)
	if s.Authorize != nil {
		if e := s.Authorize(ctx); e != nil {
			if file != nil {
				file.Close()
			}
			return errors.New("peer capability revoked or signaling unavailable")
		}
	}
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := io.CopyBuffer(conn, io.LimitReader(file, s.Info.Size), make([]byte, 32768))
	if err != nil {
		return err
	}
	if n != s.Info.Size {
		return errors.New("source changed")
	}
	return nil
}
func Receive(ctx context.Context, info Info, token, dest string) error {
	if !privateAddress(info.Address) || len(token) != 64 || info.Size < 0 || info.Size > 1<<40 || len(info.Hash) != 64 || len(info.Fingerprint) != 64 || info.Expires <= time.Now().Unix() {
		return errors.New("invalid or expired signal")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(c tls.ConnectionState) error {
		if len(c.PeerCertificates) != 1 {
			return errors.New("invalid peer certificate")
		}
		fp := sha256.Sum256(c.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(fp[:])), []byte(info.Fingerprint)) != 1 {
			return errors.New("peer fingerprint mismatch")
		}
		return nil
	}} // Trust is pinned to the fingerprint received via the capability signal.
	conn, err := (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", info.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = io.WriteString(conn, token); err != nil {
		return err
	}
	if _, err = os.Lstat(dest); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".netdisk-peer-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(tmp, h), io.LimitReader(conn, info.Size+1), make([]byte, 32768))
	if err != nil {
		return err
	}
	if n != info.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), info.Hash) {
		return errors.New("direct transfer hash or size mismatch")
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Link is an atomic no-overwrite publication; it protects existing data if a
	// destination appeared while the stream was in flight.
	return os.Link(tmp.Name(), dest)
}
