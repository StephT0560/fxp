// SPDX-License-Identifier: MIT
// Package tlsconfig supplies reloadable listener TLS and verified client dialing.
package tlsconfig

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type ServerOptions struct {
	CertFile, KeyFile, ClientCAFile string
	Required                        bool
}
type Server struct {
	options ServerOptions
	mu      sync.Mutex
	active  atomic.Pointer[tls.Config]
	digest  [32]byte
}

func ServerFromEnv() (*Server, error) {
	required := false
	if value := os.Getenv("FXP_TLS_REQUIRED"); value != "" {
		var err error
		required, err = strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("FXP_TLS_REQUIRED must be a boolean")
		}
	}
	return NewServer(ServerOptions{os.Getenv("FXP_TLS_CERT_FILE"), os.Getenv("FXP_TLS_KEY_FILE"), os.Getenv("FXP_TLS_CLIENT_CA_FILE"), required})
}
func NewServer(o ServerOptions) (*Server, error) {
	s := &Server{options: o}
	if o.CertFile == "" && o.KeyFile == "" {
		if o.Required || o.ClientCAFile != "" {
			return nil, errors.New("TLS requires FXP_TLS_CERT_FILE and FXP_TLS_KEY_FILE")
		}
		return s, nil
	}
	if o.CertFile == "" || o.KeyFile == "" {
		return nil, errors.New("TLS certificate and key must both be configured")
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Server) Enabled() bool { return s.active.Load() != nil }
func (s *Server) material() ([]byte, []byte, []byte, [32]byte, error) {
	var zero [32]byte
	cert, err := os.ReadFile(s.options.CertFile)
	if err != nil {
		return nil, nil, nil, zero, fmt.Errorf("read TLS certificate: %w", err)
	}
	key, err := os.ReadFile(s.options.KeyFile)
	if err != nil {
		return nil, nil, nil, zero, fmt.Errorf("read TLS private key: %w", err)
	}
	var ca []byte
	if s.options.ClientCAFile != "" {
		ca, err = os.ReadFile(s.options.ClientCAFile)
		if err != nil {
			return nil, nil, nil, zero, fmt.Errorf("read TLS client CA: %w", err)
		}
	}
	h := sha256.New()
	for _, part := range [][]byte{cert, key, ca} {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write(part)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return cert, key, ca, sum, nil
}
func (s *Server) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.options.CertFile == "" {
		return nil
	}
	cert, key, ca, sum, err := s.material()
	if err != nil {
		return err
	}
	if s.Enabled() && sum == s.digest {
		return nil
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return fmt.Errorf("invalid TLS certificate/key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("invalid TLS leaf certificate")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("TLS server certificate is expired or not yet valid")
	}
	pair.Leaf = leaf
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	if s.options.ClientCAFile != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return errors.New("TLS client CA file contains no certificates")
		}
		cfg.ClientCAs = roots
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.SessionTicketsDisabled = true
	}
	// Every snapshot is immutable. New handshakes select it atomically; established
	// connections keep their existing TLS/authentication state.
	s.active.Store(cfg)
	s.digest = sum
	return nil
}
func (s *Server) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		cfg := s.active.Load()
		if cfg == nil {
			return nil, errors.New("TLS is not configured")
		}
		return cfg, nil
	}}
}
func (s *Server) Listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if s.Enabled() {
		return tls.NewListener(ln, s.Config()), nil
	}
	if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
		slog.Warn("plaintext listener on a non-loopback address; configure TLS or FXP_TLS_REQUIRED=true", "address", ln.Addr().String())
	}
	return ln, nil
}

// Watch detects file-content changes (including atomic replacement), never
// switches to plaintext, and retains the last valid cert/CA snapshot on failure.
func (s *Server) Watch(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var attempted [32]byte
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _, _, sum, err := s.material()
			if err != nil {
				if err.Error() != lastError {
					slog.Warn("TLS reload failed; keeping previous configuration", "error", err)
					lastError = err.Error()
				}
				continue
			}
			if sum == attempted {
				continue
			}
			attempted = sum
			if err = s.Reload(); err != nil {
				slog.Warn("TLS reload failed; keeping previous configuration", "error", err)
				lastError = err.Error()
			} else {
				lastError = ""
			}
		}
	}
}
func Handshake(ctx context.Context, c net.Conn, timeout time.Duration) error {
	t, ok := c.(*tls.Conn)
	if !ok {
		return nil
	}
	if err := t.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := t.HandshakeContext(ctx); err != nil {
		return err
	}
	return t.SetDeadline(time.Time{})
}
