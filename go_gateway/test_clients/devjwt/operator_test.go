// SPDX-License-Identifier: MIT
package devjwt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func claimsOf(t *testing.T, token string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestOperatorRoleOnlyOnOperatorTokens(t *testing.T) {
	token, err := GenerateOperator("operator-test-secret-0123456789", "demo-operator", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c := claimsOf(t, token); c["fxp_role"] != "operator" || c["sub"] != "demo-operator" {
		t.Fatalf("operator claims %v", c)
	}
	if _, ok := claimsOf(t, Generate("operator-test-secret-0123456789", "T1"))["fxp_role"]; ok {
		t.Fatal("ordinary tokens must not carry a role")
	}
	if _, err := GenerateOperator("operator-test-secret-0123456789", "op", time.Hour); err == nil {
		t.Fatal("operator tokens are short-lived")
	}
}
