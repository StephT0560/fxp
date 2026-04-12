package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// generateDevJWT creates a signed HS256 JWT for local dev/testing.
// In production the client receives a token from your identity provider.
func generateDevJWT(secret, traderID string) string {
	header := base64.RawURLEncoding.EncodeToString(
		mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}),
	)
	payload := base64.RawURLEncoding.EncodeToString(
		mustJSON(map[string]interface{}{
			"sub": traderID,
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(24 * time.Hour).Unix(),
		}),
	)
	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}