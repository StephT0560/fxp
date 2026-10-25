// SPDX-License-Identifier: MIT

package devjwt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGenerateEmitsValidationClaims(t *testing.T) {
	t.Setenv("FXP_JWT_KID", "k1")
	parts := strings.Split(Generate("s", "T1"), ".")
	var h, c map[string]any
	for i, dst := range []*map[string]any{&h, &c} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
	if h["alg"] != "HS256" || h["kid"] != "k1" {
		t.Errorf("header = %v", h)
	}
	for _, k := range []string{"sub", "iss", "aud", "jti", "iat", "nbf", "exp"} {
		if c[k] == nil || c[k] == "" {
			t.Errorf("claim %s missing: %v", k, c)
		}
	}
	if c["iss"] != DefaultIssuer || c["aud"] != DefaultAudience {
		t.Errorf("iss/aud = %v/%v", c["iss"], c["aud"])
	}
}

func TestGenerateWithTTLValidation(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second, 25 * time.Hour} {
		if _, err := GenerateWithTTL("test", "demo:alice", ttl); err == nil {
			t.Fatal("invalid JWT lifetime accepted")
		}
	}
}
