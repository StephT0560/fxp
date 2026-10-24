// SPDX-License-Identifier: MIT

package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func decodeJWTPart(t *testing.T, part string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGenerateJWTClaims(t *testing.T) {
	for _, tc := range []struct{ name, kid, iss, aud string }{
		{"dev defaults", "", "", ""},
		{"configured", "k2", "corp-idp", "fxp-prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FXP_JWT_KID", tc.kid)
			t.Setenv("FXP_JWT_ISSUER", tc.iss)
			t.Setenv("FXP_JWT_AUDIENCE", tc.aud)
			tok := generateJWT("secret", "FIRM01")
			parts := strings.Split(tok, ".")
			if len(parts) != 3 {
				t.Fatalf("token %q", tok)
			}
			mac := hmac.New(sha256.New, []byte("secret"))
			mac.Write([]byte(parts[0] + "." + parts[1]))
			if parts[2] != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
				t.Fatal("bad signature")
			}
			h, c := decodeJWTPart(t, parts[0]), decodeJWTPart(t, parts[1])
			if h["alg"] != "HS256" {
				t.Errorf("alg = %v", h["alg"])
			}
			if kid, ok := h["kid"]; (tc.kid == "" && ok) || (tc.kid != "" && kid != tc.kid) {
				t.Errorf("kid = %v, want %q", kid, tc.kid)
			}
			wantIss, wantAud := tc.iss, tc.aud
			if wantIss == "" {
				wantIss, wantAud = defaultJWTIssuer, defaultJWTAudience
			}
			if c["sub"] != "FIRM01" || c["iss"] != wantIss || c["aud"] != wantAud {
				t.Errorf("claims = %v", c)
			}
			now := float64(time.Now().Unix())
			if c["nbf"].(float64) > now || c["exp"].(float64) <= now || c["iat"] == nil {
				t.Errorf("times = %v", c)
			}
			if jti, _ := c["jti"].(string); len(jti) != 32 {
				t.Errorf("jti = %v", c["jti"])
			}
			if strings.Split(generateJWT("secret", "FIRM01"), ".")[1] == parts[1] {
				t.Error("two tokens share a jti")
			}
		})
	}
}
