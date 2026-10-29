// SPDX-License-Identifier: MIT
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"go_gateway/protobuf"
	"go_gateway/tlsconfig"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Re-exec this test binary through exactly the production worker entry point.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--subscriber-worker" {
		if err := runSubscriberWorker(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeSubscriberGateway(t *testing.T, listener net.Listener, count int, delay time.Duration) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var peers []net.Conn
		defer func() {
			for _, peer := range peers {
				_ = peer.Close()
			}
			_ = listener.Close()
		}()
		for i := 0; i < count; i++ {
			peer, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			peers = append(peers, peer)
			_ = peer.SetDeadline(time.Now().Add(10 * time.Second))
			message, err := readFramed(peer)
			if err != nil {
				done <- err
				return
			}
			if message.GetLogon() == nil {
				done <- fmt.Errorf("expected Logon")
				return
			}
			time.Sleep(delay)
			if err = sendFramed(peer, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_LogonAck{LogonAck: &protobuf.LogonAck{Accepted: true}}}); err != nil {
				done <- err
				return
			}
			message, err = readFramed(peer)
			if err != nil {
				done <- err
				return
			}
			if message.GetMarketDataRequest() == nil {
				done <- fmt.Errorf("expected subscription")
				return
			}
			if err = sendFramed(peer, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataSnapshot{MarketDataSnapshot: &protobuf.MarketDataSnapshot{Symbol: "TEST"}}}); err != nil {
				done <- err
				return
			}
		}
		for _, peer := range peers {
			var data [1]byte
			_, err := peer.Read(data[:])
			if err != io.EOF {
				done <- fmt.Errorf("expected worker close, got %v", err)
				return
			}
		}
		done <- nil
	}()
	return done
}

func TestSubscriberProcessPreservesPrivateCATLSAndMTLS(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("OpenSSL fixture prerequisite")
	}
	directory := t.TempDir()
	command := exec.Command("bash", "../../scripts/gen_test_certs.sh", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("certificates: %v %s", err, output)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(directory, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid CA")
	}
	for _, mtls := range []bool{false, true} {
		t.Run(fmt.Sprint(mtls), func(t *testing.T) {
			server := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			options := tlsconfig.ClientOptions{Enabled: true, CAFile: filepath.Join(directory, "ca.crt"), ServerName: "localhost"}
			if mtls {
				server.ClientAuth = tls.RequireAndVerifyClientCert
				server.ClientCAs = roots
				options.CertFile = filepath.Join(directory, "client.crt")
				options.KeyFile = filepath.Join(directory, "client.key")
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", server)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			done := fakeSubscriberGateway(t, listener, 1, 0)
			c := config{TLS: options, Address: listener.Addr().String(), Subscribers: 1, Concurrency: 1, Symbol: "TEST", Symbols: "single", Timeout: 5 * time.Second}
			public, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(public), directory) || strings.Contains(string(public), "localhost") {
				t.Fatal("public report leaked TLS paths/name")
			}
			child, err := startSubscriberProcess(c, "tls-fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Close(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscriberStartupBoundCoversSequentialPerReaderDeadlines(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := fakeSubscriberGateway(t, listener, 2, 150*time.Millisecond)
	c := config{Address: listener.Addr().String(), Subscribers: 2, Concurrency: 1, Symbol: "TEST", Symbols: "single", Timeout: 250 * time.Millisecond}
	// Each reader stays within its own deadline; cumulative setup exceeds it.
	child, err := startSubscriberProcess(c, "slow-setup")
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
