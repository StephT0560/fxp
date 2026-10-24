// SPDX-License-Identifier: MIT

// Package devjwt mints HS256 JWTs for the local test clients, the benchmark
// and the trading-day simulation.
package devjwt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Defaults match the gateway's dev defaults (transport.DefaultJWTIssuer /
// DefaultJWTAudience).
const (
	DefaultIssuer   = "fxp-dev"
	DefaultAudience = "fxp-gateway"
)

// Generate creates a signed HS256 JWT for local dev/testing. iss, aud and kid
// come from FXP_JWT_ISSUER, FXP_JWT_AUDIENCE and FXP_JWT_KID (same variables
// and defaults as the gateway); every token gets a fresh jti.
// In production the client receives a token from your identity provider.
func Generate(secret, traderID string) string {
	head := map[string]string{"alg": "HS256", "typ": "JWT"}
	if kid := os.Getenv("FXP_JWT_KID"); kid != "" {
		head["kid"] = kid
	}
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString(mustJSON(head))
	payload := base64.RawURLEncoding.EncodeToString(
		mustJSON(map[string]interface{}{
			"sub": traderID,
			"iss": envOr("FXP_JWT_ISSUER", DefaultIssuer),
			"aud": envOr("FXP_JWT_AUDIENCE", DefaultAudience),
			"jti": newJTI(),
			"iat": now.Unix(),
			"nbf": now.Unix(),
			"exp": now.Add(24 * time.Hour).Unix(),
		}),
	)
	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func newJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("devjwt: jti: %v", err))
	}
	return hex.EncodeToString(b)
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}
