// SPDX-License-Identifier: MIT
package startup

import (
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, jwt, previous, token, addr string
		denied                           bool
	}{
		{"missing", "", "", "", "127.0.0.1:0", true},
		{"short", strings.Repeat("x", 23), "", "", "127.0.0.1:0", true},
		{"minimum", strings.Repeat("x", 24), "", "", "127.0.0.1:0", false},
		{"dev local", "dev-secret-change-in-production", "", "", "127.0.0.1:0", false},
		{"dev external", "dev-secret-change-in-production", "", "", "0.0.0.0:0", true},
		{"dev ipv6", "dev-secret-change-in-production", "", "", "[::]:0", true},
		{"mixed listeners", "dev-secret-change-in-production", "", "", "127.0.0.1:0,0.0.0.0:0", true},
		{"previous short", strings.Repeat("x", 24), "short", "", "127.0.0.1:0", true},
		{"previous dev", strings.Repeat("x", 24), "dev-secret-change-in-production", "", "0.0.0.0:0", true},
		{"token short", strings.Repeat("x", 24), "", "short", "127.0.0.1:0", true},
		{"token minimum", strings.Repeat("x", 24), "", strings.Repeat("y", 24), "0.0.0.0:0", false},
		{"token oversized", strings.Repeat("x", 24), "", strings.Repeat("y", 1025), "127.0.0.1:0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FXP_JWT_SECRET", tc.jwt)
			t.Setenv("FXP_JWT_PREVIOUS_SECRET", tc.previous)
			t.Setenv("FXP_CORE_AUTH_TOKEN", tc.token)
			t.Setenv("FXP_ALLOW_PLAINTEXT", "true") // plaintext override never weakens secrets
			err := ValidateSecrets(strings.Split(tc.addr, ",")...)
			if (err != nil) != tc.denied {
				t.Fatalf("denied=%v error=%v", tc.denied, err)
			}
			if err != nil && (strings.Contains(err.Error(), tc.jwt) && tc.jwt != "") {
				t.Fatal("secret echoed")
			}
		})
	}
}
