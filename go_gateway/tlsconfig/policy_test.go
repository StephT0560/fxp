// SPDX-License-Identifier: MIT
package tlsconfig

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestListenerPolicy(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		allow  bool
		denied bool
	}{
		{"0.0.0.0:0", false, true}, {"[::]:0", false, true},
		{"0.0.0.0:0", true, false}, {"127.0.0.1:0", false, false}, {"[::1]:0", false, false},
	} {
		t.Run(tc.addr+strings.Repeat("allow", btoi(tc.allow)), func(t *testing.T) {
			t.Setenv("FXP_ALLOW_PLAINTEXT", "false")
			if tc.allow {
				t.Setenv("FXP_ALLOW_PLAINTEXT", "true")
			}
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			server, err := ServerFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := server.Listen(tc.addr)
			if ln != nil {
				defer func() { _ = ln.Close() }()
			}
			if (err != nil) != tc.denied {
				t.Fatalf("error=%v denied=%v", err, tc.denied)
			}
			if tc.allow && !strings.Contains(logs.String(), "FXP_ALLOW_PLAINTEXT") {
				t.Fatal("override must warn")
			}
		})
	}
}
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestNonLoopbackTLSNeedsNoPlaintextOverride(t *testing.T) {
	p := newPKI(t)
	t.Setenv("FXP_ALLOW_PLAINTEXT", "false")
	server, err := NewServer(ServerOptions{CertFile: p.cert, KeyFile: p.key})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := server.Listen("0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
}

func TestMalformedPlaintextOverrideFails(t *testing.T) {
	t.Setenv("FXP_ALLOW_PLAINTEXT", "yes")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("malformed override accepted")
	}
}

func TestOverrideWarningCannotBeMutedByLogLevel(t *testing.T) {
	t.Setenv("FXP_ALLOW_PLAINTEXT", "true")
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(oldLogger)
	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() { os.Stderr = oldStderr; _ = reader.Close(); _ = writer.Close() }()
	server, err := NewServer(ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen("0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	_ = writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "FXP_ALLOW_PLAINTEXT") {
		t.Fatal("startup warning suppressed by log level")
	}
}
