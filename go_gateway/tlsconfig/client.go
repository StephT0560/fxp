// SPDX-License-Identifier: MIT
package tlsconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

type ClientOptions struct {
	Enabled                               bool
	CAFile, CertFile, KeyFile, ServerName string
}

func ClientFromEnv(prefix string) (ClientOptions, error) {
	var o ClientOptions
	value := os.Getenv(prefix + "TLS")
	if value != "" {
		var err error
		o.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return o, fmt.Errorf("%sTLS must be a boolean", prefix)
		}
	}
	o.CAFile = os.Getenv(prefix + "TLS_CA_FILE")
	o.CertFile = os.Getenv(prefix + "TLS_CERT_FILE")
	o.KeyFile = os.Getenv(prefix + "TLS_KEY_FILE")
	o.ServerName = os.Getenv(prefix + "TLS_SERVER_NAME")
	return o, nil
}
func (o *ClientOptions) Flags(f *flag.FlagSet) {
	f.BoolVar(&o.Enabled, "tls", o.Enabled, "use verified TLS")
	f.StringVar(&o.CAFile, "tls-ca", o.CAFile, "trusted CA PEM (default system roots)")
	f.StringVar(&o.CertFile, "tls-cert", o.CertFile, "mTLS client certificate PEM")
	f.StringVar(&o.KeyFile, "tls-key", o.KeyFile, "mTLS client key PEM")
	f.StringVar(&o.ServerName, "tls-server-name", o.ServerName, "verified TLS server name (default dial host)")
}
func (o ClientOptions) Config() (*tls.Config, error) {
	if !o.Enabled {
		if o.CAFile != "" || o.CertFile != "" || o.KeyFile != "" || o.ServerName != "" {
			return nil, errors.New("TLS client settings require TLS enabled")
		}
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.ServerName}
	if o.CAFile != "" {
		raw, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("TLS CA contains no certificates")
		}
		cfg.RootCAs = roots
	}
	if (o.CertFile == "") != (o.KeyFile == "") {
		return nil, errors.New("TLS client certificate and key must both be configured")
	}
	if o.CertFile != "" {
		pair, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
func (o ClientOptions) Dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	cfg, err := o.Config()
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}
	if cfg == nil {
		return dialer.DialContext(ctx, "tcp", addr)
	}
	return (&tls.Dialer{NetDialer: dialer, Config: cfg}).DialContext(ctx, "tcp", addr)
}

// Version is zero for plaintext connections. Dial completes TLS negotiation.
func Version(conn net.Conn) uint16 {
	if c, ok := conn.(*tls.Conn); ok {
		return c.ConnectionState().Version
	}
	return 0
}
