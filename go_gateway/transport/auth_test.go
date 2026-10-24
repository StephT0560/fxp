// SPDX-License-Identifier: MIT

package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func signJWT(t *testing.T, secret string, header, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	in := enc(header) + "." + enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(in))
	return in + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestValidateJWT(t *testing.T) {
	const cur, prev = "current-key", "previous-key"
	t.Setenv("FXP_JWT_SECRET", cur)
	t.Setenv("FXP_JWT_KID", "k2")
	t.Setenv("FXP_JWT_PREVIOUS_KID", "k1")
	t.Setenv("FXP_JWT_PREVIOUS_SECRET", prev)
	t.Setenv("FXP_JWT_ISSUER", "fxp-test")
	t.Setenv("FXP_JWT_AUDIENCE", "fxp-gw-test")
	t.Setenv("FXP_JWT_LEEWAY_SEC", "5")

	now := time.Now().Unix()
	claims := func(mut func(map[string]any)) map[string]any {
		c := map[string]any{"sub": "T1", "iss": "fxp-test", "aud": "fxp-gw-test", "iat": now, "nbf": now, "exp": now + 60, "jti": "j"}
		if mut != nil {
			mut(c)
		}
		return c
	}
	hs := func(kid string) map[string]any {
		h := map[string]any{"alg": "HS256", "typ": "JWT"}
		if kid != "" {
			h["kid"] = kid
		}
		return h
	}
	unsigned := func(alg string) string {
		e := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
		return e(map[string]any{"alg": alg, "typ": "JWT"}) + "." + e(claims(nil)) + "."
	}

	tests := []struct {
		name, token, wantErr string
	}{
		{"valid current key without kid", signJWT(t, cur, hs(""), claims(nil)), ""},
		{"valid current kid", signJWT(t, cur, hs("k2"), claims(nil)), ""},
		{"rotated previous kid", signJWT(t, prev, hs("k1"), claims(nil)), ""},
		{"previous key without kid", signJWT(t, prev, hs(""), claims(nil)), "signature"},
		{"previous key under current kid", signJWT(t, prev, hs("k2"), claims(nil)), "signature"},
		{"unknown kid", signJWT(t, cur, hs("k9"), claims(nil)), "kid"},
		{"alg none", unsigned("none"), "alg"},
		{"alg None", unsigned("None"), "alg"},
		{"alg none signed", signJWT(t, cur, map[string]any{"alg": "none"}, claims(nil)), "alg"},
		{"alg HS512", signJWT(t, cur, map[string]any{"alg": "HS512"}, claims(nil)), "alg"},
		{"alg RS256", signJWT(t, cur, map[string]any{"alg": "RS256"}, claims(nil)), "alg"},
		{"alg missing", signJWT(t, cur, map[string]any{"typ": "JWT"}, claims(nil)), "alg"},
		{"wrong secret", signJWT(t, "other", hs(""), claims(nil)), "signature"},
		{"wrong iss", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["iss"] = "evil" })), "iss"},
		{"missing iss", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "iss") })), "iss"},
		{"wrong aud", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["aud"] = "other" })), "aud"},
		{"missing aud", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "aud") })), "aud"},
		{"aud array containing ours", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["aud"] = []string{"x", "fxp-gw-test"} })), ""},
		{"aud array without ours", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["aud"] = []string{"x", "y"} })), "aud"},
		{"expired", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["exp"] = now - 1 })), "expired"},
		{"nbf in the future", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["nbf"] = now + 60 })), "nbf"},
		{"nbf within leeway", signJWT(t, cur, hs(""), claims(func(c map[string]any) { c["nbf"] = now + 3 })), ""},
		{"no nbf", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "nbf") })), ""},
		{"missing sub", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "sub") })), "sub"},
		{"missing exp", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "exp") })), "exp"},
		{"missing iat", signJWT(t, cur, hs(""), claims(func(c map[string]any) { delete(c, "iat") })), "iat"},
		{"malformed", "a.b", "malformed"},
		{"bad header encoding", "!!." + strings.SplitN(signJWT(t, cur, hs(""), claims(nil)), ".", 2)[1], "header"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := validateJWT(tc.token)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c.Sub != "T1" || c.Jti != "j" {
					t.Fatalf("claims = %+v", c)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateJWTDevDefaults(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", "s")
	now := time.Now().Unix()
	tok := signJWT(t, "s", map[string]any{"alg": "HS256"}, map[string]any{
		"sub": "T", "iss": DefaultJWTIssuer, "aud": DefaultJWTAudience, "iat": now, "exp": now + 60,
	})
	if _, err := validateJWT(tok); err != nil {
		t.Fatalf("dev-default token rejected: %v", err)
	}
}

func TestJWTReplayCache(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", "s")
	now := time.Now().Unix()
	mint := func(jti string) string {
		c := map[string]any{"sub": "T", "iss": DefaultJWTIssuer, "aud": DefaultJWTAudience, "iat": now, "exp": now + 60}
		if jti != "" {
			c["jti"] = jti
		}
		return signJWT(t, "s", map[string]any{"alg": "HS256"}, c)
	}
	reused := mint("replay-1")
	// Off by default: a token may be presented again.
	for i := 0; i < 2; i++ {
		if _, err := validateJWT(reused); err != nil {
			t.Fatalf("replay cache off: attempt %d rejected: %v", i, err)
		}
	}

	t.Setenv("FXP_JWT_REPLAY_CACHE", "on")
	resetReplayCache()
	t.Cleanup(resetReplayCache)
	if _, err := validateJWT(reused); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	if _, err := validateJWT(reused); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("second use: got %v, want replay error", err)
	}
	if _, err := validateJWT(mint("replay-2")); err != nil {
		t.Fatalf("fresh jti rejected: %v", err)
	}
	if _, err := validateJWT(mint("")); err == nil || !strings.Contains(err.Error(), "jti") {
		t.Fatalf("missing jti with replay cache on: got %v", err)
	}
}

func TestWSCheckOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(handleConnections))
	defer srv.Close()
	self := srv.URL // http://127.0.0.1:port

	dial := func(origin string) (int, error) {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), h)
		if c != nil {
			_ = c.Close()
		}
		if resp != nil {
			return resp.StatusCode, err
		}
		return 0, err
	}

	tests := []struct {
		name, allow, origin string
		ok                  bool
	}{
		{"missing Origin (non-browser client)", "", "", true},
		{"same origin by default", "", self, true},
		{"cross origin denied by default", "", "http://evil.example", false},
		{"allow-listed origin", "https://trader.example, http://ops.example:8443", "http://ops.example:8443", true},
		{"allow-list is exact", "https://trader.example", "https://trader.example.evil", false},
		{"allow-list match ignores case", "https://Trader.Example", "https://trader.example", true},
		{"not on allow-list", "https://trader.example", "http://evil.example", false},
		{"same origin still allowed with a list", "https://trader.example", self, true},
		{"malformed Origin", "", "://", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FXP_GW_WS_ALLOWED_ORIGINS", tc.allow)
			status, err := dial(tc.origin)
			if tc.ok && err != nil {
				t.Fatalf("handshake failed: status %d, %v", status, err)
			}
			if !tc.ok && (err == nil || status != http.StatusForbidden) {
				t.Fatalf("handshake: status %d, err %v; want 403", status, err)
			}
		})
	}
}

func TestJWTReplayCacheScopedByKey(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", "cur")
	t.Setenv("FXP_JWT_PREVIOUS_KID", "k1")
	t.Setenv("FXP_JWT_PREVIOUS_SECRET", "prev")
	t.Setenv("FXP_JWT_REPLAY_CACHE", "on")
	resetReplayCache()
	t.Cleanup(resetReplayCache)
	now := time.Now().Unix()
	c := map[string]any{"sub": "T", "iss": DefaultJWTIssuer, "aud": DefaultJWTAudience, "iat": now, "exp": now + 60, "jti": "same"}
	underPrev := signJWT(t, "prev", map[string]any{"alg": "HS256", "kid": "k1"}, c)
	underCur := signJWT(t, "cur", map[string]any{"alg": "HS256"}, c)
	if _, err := validateJWT(underPrev); err != nil {
		t.Fatalf("previous-key token rejected: %v", err)
	}
	if _, err := validateJWT(underCur); err != nil {
		t.Fatalf("current-key token with the same jti rejected: %v", err)
	}
	for _, tok := range []string{underPrev, underCur} {
		if _, err := validateJWT(tok); err == nil || !strings.Contains(err.Error(), "replay") {
			t.Fatalf("replay: got %v", err)
		}
	}
}

func TestReplayCachePrunesExpired(t *testing.T) {
	resetReplayCache()
	t.Cleanup(resetReplayCache)
	c := replayCache
	if !c.firstUse("a", 100, 50) || !c.firstUse("b", 300, 50) {
		t.Fatal("fresh ids rejected")
	}
	if c.firstUse("a", 100, 60) {
		t.Fatal("replay of a live id accepted")
	}
	if !c.firstUse("c", 400, 150) {
		t.Fatal("fresh id rejected")
	}
	if _, ok := c.seen["a"]; ok || len(c.seen) != 2 {
		t.Fatalf("expired entry not pruned: %v", c.seen)
	}
	if !c.firstUse("a", 500, 150) {
		t.Fatal("id reuse after expiry rejected")
	}
}

func BenchmarkReplayCacheFirstUse(b *testing.B) {
	resetReplayCache()
	b.Cleanup(resetReplayCache)
	now := time.Now().Unix()
	for i := 0; i < 200_000; i++ {
		replayCache.firstUse("warm-"+strconv.Itoa(i), now+86400, now)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		replayCache.firstUse("b-"+strconv.Itoa(i), now+86400, now)
	}
}

func TestCheckOriginScheme(t *testing.T) {
	t.Setenv("FXP_GW_WS_ALLOWED_ORIGINS", "")
	for _, tc := range []struct {
		name, xfp, origin string
		tls, ok           bool
	}{
		{"plain http same scheme", "", "http://gw.example", false, true},
		{"https page vs plain http listener", "", "https://gw.example", false, false},
		{"tls listener, https page", "", "https://gw.example", true, true},
		{"tls listener, http page", "", "http://gw.example", true, false},
		{"proxy https, https page", "https", "https://gw.example", false, true},
		{"proxy https, http page", "https", "http://gw.example", false, false},
		{"proxy list uses first hop", "https, http", "https://gw.example", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://gw.example/ws", nil)
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.xfp != "" {
				r.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			r.Header.Set("Origin", tc.origin)
			if got := checkOrigin(r); got != tc.ok {
				t.Fatalf("checkOrigin = %v, want %v", got, tc.ok)
			}
		})
	}
}
