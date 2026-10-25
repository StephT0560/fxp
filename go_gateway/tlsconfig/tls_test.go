// SPDX-License-Identifier: MIT
package tlsconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type pki struct {
	ca, cert, key, clientCert, clientKey string
	root                                 *x509.Certificate
	signer                               *ecdsa.PrivateKey
	dir                                  string
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	p := &pki{dir: t.TempDir()}
	p.signer, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "throwaway CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &p.signer.PublicKey, p.signer)
	if err != nil {
		t.Fatal(err)
	}
	p.root, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p.ca = filepath.Join(p.dir, "ca.pem")
	write(t, p.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	p.cert, p.key = p.leaf(t, "server", 2, false, false)
	p.clientCert, p.clientKey = p.leaf(t, "client", 3, true, false)
	return p
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func (p *pki) leaf(t *testing.T, name string, serial int64, client, expired bool) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	usage := x509.ExtKeyUsageServerAuth
	if client {
		usage = x509.ExtKeyUsageClientAuth
	}
	until := time.Now().Add(time.Hour)
	if expired {
		until = time.Now().Add(-time.Minute)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: time.Now().Add(-time.Hour), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, p.root, &key.PublicKey, p.signer)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(p.dir, name+".pem"), filepath.Join(p.dir, name+".key")
	write(t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}))
	return certFile, keyFile
}
func serve(t *testing.T, s *Server) (string, func()) {
	t.Helper()
	ln, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				if Handshake(ctx, c, time.Second) != nil {
					return
				}
				var b [1]byte
				if _, err := io.ReadFull(c, b[:]); err == nil {
					_, _ = c.Write(b[:])
				}
			}()
		}
	}()
	return ln.Addr().String(), func() { cancel(); _ = ln.Close(); <-done }
}
func exchange(addr string, options ClientOptions, max uint16) (uint16, int64, error) {
	cfg, err := options.Config()
	if err != nil {
		return 0, 0, err
	}
	cfg.MaxVersion = max
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, cfg)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err = c.Write([]byte{7}); err != nil {
		return 0, 0, err
	}
	var b [1]byte
	if _, err = io.ReadFull(c, b[:]); err != nil {
		return 0, 0, err
	}
	return c.ConnectionState().Version, c.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), nil
}
func TestTLSHandshakeTrustAndVersionFloor(t *testing.T) {
	p := newPKI(t)
	s, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key})
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := serve(t, s)
	defer stop()
	options := ClientOptions{Enabled: true, CAFile: p.ca, ServerName: "localhost"}
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		got, _, err := exchange(addr, options, version)
		if err != nil || got != version {
			t.Fatalf("version %x: %x %v", version, got, err)
		}
	}
	other := newPKI(t)
	bad := options
	bad.CAFile = other.ca
	if _, _, err := exchange(addr, bad, 0); err == nil {
		t.Fatal("wrong CA accepted")
	}
	cfg, _ := options.Config()
	cfg.MinVersion = tls.VersionTLS10
	cfg.MaxVersion = tls.VersionTLS11
	if c, err := tls.Dial("tcp", addr, cfg); err == nil {
		_ = c.Close()
		t.Fatal("TLS 1.1 accepted")
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte("not TLS\n"))
	var b [1]byte
	if _, err = c.Read(b[:]); err == nil {
		t.Fatal("plaintext accepted")
	}
}
func TestMTLSAndExpiredCertificates(t *testing.T) {
	p := newPKI(t)
	s, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key, ClientCAFile: p.ca})
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := serve(t, s)
	defer stop()
	options := ClientOptions{Enabled: true, CAFile: p.ca, ServerName: "localhost"}
	if _, _, err := exchange(addr, options, 0); err == nil {
		t.Fatal("missing client cert accepted")
	}
	options.CertFile = p.clientCert
	options.KeyFile = p.clientKey
	if _, _, err := exchange(addr, options, 0); err != nil {
		t.Fatal(err)
	}
	options.CertFile, options.KeyFile = p.leaf(t, "expired-client", 4, true, true)
	if _, _, err := exchange(addr, options, 0); err == nil {
		t.Fatal("expired client accepted")
	}
	cert, key := p.leaf(t, "expired-server", 5, false, true)
	if _, err := NewServer(ServerOptions{CertFile: cert, KeyFile: key}); err == nil {
		t.Fatal("expired server certificate loaded")
	}
}
func TestReloadAtomicPairAndWatcher(t *testing.T) {
	p := newPKI(t)
	s, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key})
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := serve(t, s)
	defer stop()
	options := ClientOptions{Enabled: true, CAFile: p.ca, ServerName: "localhost"}
	_, serial, err := exchange(addr, options, 0)
	if err != nil || serial != 2 {
		t.Fatal(serial, err)
	}
	newCert, newKey := p.leaf(t, "replacement", 9, false, false)
	certBytes, _ := os.ReadFile(newCert)
	keyBytes, _ := os.ReadFile(newKey)
	write(t, p.cert, certBytes)
	if err := s.Reload(); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	if _, serial, err := exchange(addr, options, 0); err != nil || serial != 2 {
		t.Fatal("last valid config lost", serial, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, 10*time.Millisecond)
	write(t, p.key, keyBytes)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, serial, err = exchange(addr, options, 0)
		if err == nil && serial == 9 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("watcher did not reload", serial, err)
}
func TestRequiredAndIncompleteConfigurationFailClosed(t *testing.T) {
	for _, o := range []ServerOptions{{Required: true}, {CertFile: "missing"}, {KeyFile: "missing"}, {ClientCAFile: "missing"}} {
		if _, err := NewServer(o); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", o)
		}
	}
}

func TestClientRejectsExpiredPeerAndWrongServerName(t *testing.T) {
	p := newPKI(t)
	cert, key := p.leaf(t, "expired-peer", 8, false, true)
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	// Bypass our startup guard to simulate an arbitrary expired external peer.
	s := &Server{}
	s.active.Store(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
	addr, stop := serve(t, s)
	defer stop()
	if _, _, err := exchange(addr, ClientOptions{Enabled: true, CAFile: p.ca, ServerName: "localhost"}, 0); err == nil {
		t.Fatal("expired peer trusted")
	}
	valid, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key})
	if err != nil {
		t.Fatal(err)
	}
	addr, stop2 := serve(t, valid)
	defer stop2()
	if _, _, err := exchange(addr, ClientOptions{Enabled: true, CAFile: p.ca, ServerName: "wrong.example"}, 0); err == nil {
		t.Fatal("hostname verification bypassed")
	}
}
func TestClientCAReloadChangesNewHandshakeAdmission(t *testing.T) {
	p := newPKI(t)
	other := newPKI(t)
	clientCA := filepath.Join(p.dir, "client-ca.pem")
	raw, _ := os.ReadFile(p.ca)
	write(t, clientCA, raw)
	s, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key, ClientCAFile: clientCA})
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := serve(t, s)
	defer stop()
	options := ClientOptions{Enabled: true, CAFile: p.ca, CertFile: p.clientCert, KeyFile: p.clientKey, ServerName: "localhost"}
	if _, _, err := exchange(addr, options, 0); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(other.ca)
	write(t, clientCA, raw)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exchange(addr, options, 0); err == nil {
		t.Fatal("removed client issuer remains trusted")
	}
	options.CertFile = other.clientCert
	options.KeyFile = other.clientKey
	if _, _, err := exchange(addr, options, 0); err != nil {
		t.Fatal(err)
	}
	if !s.active.Load().SessionTicketsDisabled {
		t.Fatal("mTLS CA changes could be bypassed by resumed sessions")
	}
}
func TestRequiredEnvironmentRejectsPlaintextAndInvalidBoolean(t *testing.T) {
	for _, key := range []string{"FXP_TLS_CERT_FILE", "FXP_TLS_KEY_FILE", "FXP_TLS_CLIENT_CA_FILE"} {
		t.Setenv(key, "")
	}
	t.Setenv("FXP_TLS_REQUIRED", "true")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("TLS-required startup accepted plaintext")
	}
	t.Setenv("FXP_TLS_REQUIRED", "invalid")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("invalid TLS-required value ignored")
	}
}
