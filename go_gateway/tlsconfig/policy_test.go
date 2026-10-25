// SPDX-License-Identifier: MIT
package tlsconfig

import (
	"bytes"
	"log/slog"
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
				defer ln.Close()
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
	defer ln.Close()
}

func TestMalformedPlaintextOverrideFails(t *testing.T) {
	t.Setenv("FXP_ALLOW_PLAINTEXT", "yes")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("malformed override accepted")
	}
}
