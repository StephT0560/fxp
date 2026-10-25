// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testApp(t *testing.T, update func(*config)) (*app, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	cfg := defaultConfig()
	cfg.secret = strings.Repeat("test-", 8)
	cfg.origin = "http://" + server.Listener.Addr().String()
	cfg.staticDir = filepath.Join(t.TempDir(), "dist")
	if update != nil {
		update(&cfg)
	}
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = a
	server.Start()
	t.Cleanup(func() { a.close(); server.Close() })
	return a, server
}
func request(t *testing.T, server *httptest.Server, method, path, body, origin string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data, res.Header
}
func TestTokenClaimsAndRateLimit(t *testing.T) {
	t.Setenv("FXP_JWT_ISSUER", "test-issuer")
	t.Setenv("FXP_JWT_AUDIENCE", "test-audience")
	t.Setenv("FXP_JWT_KID", "test-key")
	a, server := testApp(t, func(c *config) { c.tokenTTL = 30 * time.Second; c.tokenRate = 2 })
	previousJTI := ""
	for range 2 {
		status, body, headers := request(t, server, "POST", "/api/token", `{"user":"alice"}`, server.URL)
		if status != 200 || bytes.Contains(body, []byte(a.cfg.secret)) || headers.Get("Cache-Control") != "no-store" {
			t.Fatal("invalid token response")
		}
		var response struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(response.Token, ".")
		if len(parts) != 3 {
			t.Fatal("invalid JWT")
		}
		var header, claims map[string]any
		for i, dst := range []*map[string]any{&header, &claims} {
			b, err := base64.RawURLEncoding.DecodeString(parts[i])
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, dst); err != nil {
				t.Fatal(err)
			}
		}
		// Independently verify the shared helper's signature with the standard library.
		mac := hmac.New(sha256.New, []byte(a.cfg.secret))
		_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
			t.Fatal("signature invalid")
		}
		if header["alg"] != "HS256" || header["kid"] != "test-key" || claims["sub"] != "demo:alice" || claims["iss"] != "test-issuer" || claims["aud"] != "test-audience" {
			t.Fatal("incorrect claims")
		}
		if claims["exp"].(float64)-claims["iat"].(float64) != 30 || claims["nbf"] != claims["iat"] || response.ExpiresIn != 30 {
			t.Fatal("wrong lifetime")
		}
		jti := claims["jti"].(string)
		if jti == "" || jti == previousJTI {
			t.Fatal("jti reused")
		}
		previousJTI = jti
	}
	status, _, h := request(t, server, "POST", "/api/token", `{"user":"alice"}`, server.URL)
	if status != 429 || h.Get("Retry-After") == "" {
		t.Fatal("rate limit missing")
	}
}
func TestRateLimiterExpiryAndCapacity(t *testing.T) {
	l := rateLimiter{limit: 1}
	now := time.Now()
	if !l.allow("one", now) || l.allow("one", now) || !l.allow("two", now) || !l.allow("one", now.Add(time.Minute)) {
		t.Fatal("incorrect per-IP window")
	}
	l.entries = make(map[string]rateEntry)
	for i := range 4096 {
		l.entries[string(rune(i))] = rateEntry{start: now, count: 1}
	}
	if l.allow("new", now) {
		t.Fatal("unbounded IP table")
	}
	if !l.allow("new", now.Add(time.Minute)) {
		t.Fatal("expired IPs not evicted")
	}
}
func TestOriginAndJSONValidation(t *testing.T) {
	_, server := testApp(t, nil)
	for _, tc := range []struct {
		path, body, origin string
		code               int
	}{
		{"/api/token", `{"user":"unknown"}`, server.URL, 400},
		{"/api/token", `{"user":"alice","secret":"bad"}`, server.URL, 400},
		{"/api/token", `{"user":"alice"} {}`, server.URL, 400},
		{"/api/token", `{"user":"alice"}`, "http://foreign.example", 403},
		{"/api/sim/start", `{"seed":7,"symbols":2,"participants":4,"duration":301}`, server.URL, 400},
	} {
		status, _, _ := request(t, server, "POST", tc.path, tc.body, tc.origin)
		if status != tc.code {
			t.Fatalf("%s: status %d want %d", tc.path, status, tc.code)
		}
	}
	for _, origin := range []string{"", "http://foreign.example", server.URL + "/path"} {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", h)
		if c != nil {
			_ = c.Close()
		}
		if err == nil || res.StatusCode != 403 {
			t.Fatal("foreign/missing Origin accepted")
		}
		_ = res.Body.Close()
	}
	req := httptest.NewRequest("GET", "http://evil.example/api/users", nil)
	w := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
}
func TestSymbolsAndStatic(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "symbols.csv")
	if err := os.WriteFile(file, []byte("P4,4,5,10,20,1000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, server := testApp(t, func(c *config) { c.symbolFile = file })
	code, data, _ := request(t, server, "GET", "/api/symbols", "", "")
	if code != 200 {
		t.Fatal(code)
	}
	var symbols []symbolView
	if err := json.Unmarshal(data, &symbols); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range symbols {
		if s.Symbol == "P4" {
			found = true
			if s.TickSize != "5" || s.PriceScale != 4 || s.LotSize != 10 || s.MinQuantity != 20 || s.MaxQuantity != 1000 {
				t.Fatal("symbol rules changed")
			}
		}
	}
	if !found {
		t.Fatal("configured symbol missing")
	}
	_, placeholder, h := request(t, server, "GET", "/", "", "")
	if !bytes.Contains(placeholder, []byte("UI has not been built")) || h.Get("Content-Security-Policy") == "" {
		t.Fatal("placeholder/headers missing")
	}
	if err := os.MkdirAll(a.cfg.staticDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.staticDir, "index.html"), []byte("spa marker"), 0600); err != nil {
		t.Fatal(err)
	}
	code, data, _ = request(t, server, "GET", "/orders/working", "", "")
	if code != 200 || string(data) != "spa marker" {
		t.Fatal("SPA fallback missing")
	}
	code, _, _ = request(t, server, "GET", "/missing.js", "", "")
	if code != 404 {
		t.Fatal("missing asset served as HTML")
	}
	code, _, _ = request(t, server, "GET", "/api/unknown", "", "")
	if code != 404 {
		t.Fatal("unknown API served as HTML")
	}
}
func TestProxyPreservesIndependentRustBytesAndClose(t *testing.T) {
	raw, err := os.ReadFile("../tests/vectors/trade_capture_report_v2_5.rust.hex")
	if err != nil {
		t.Fatal(err)
	}
	vector, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	originSeen := make(chan string, 1)
	upstreamClosed := make(chan bool, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originSeen <- r.Header.Get("Origin")
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(data, vector) {
			upstreamClosed <- false
			return
		}
		if err := conn.WriteMessage(kind, data); err != nil {
			upstreamClosed <- false
			return
		}
		_, _, err = conn.ReadMessage()
		upstreamClosed <- websocket.IsCloseError(err, 1000)
	}))
	defer upstream.Close()
	a, server := testApp(t, func(c *config) { c.upstream = "ws" + strings.TrimPrefix(upstream.URL, "http") })
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.WriteMessage(websocket.BinaryMessage, vector); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	kind, got, err := client.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(got, vector) {
		t.Fatal("proxy changed binary message")
	}
	if <-originSeen != "" {
		t.Fatal("browser Origin leaked upstream")
	}
	_ = client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "done"), time.Now().Add(time.Second))
	select {
	case ok := <-upstreamClosed:
		if !ok {
			t.Fatal("close not propagated")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not closed")
	}
	a.close()
}

func TestProxyLimitsAndShutdown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kind, size, code int
	}{{"text", websocket.TextMessage, 4, 1003}, {"oversize", websocket.BinaryMessage, maxWSMessage + 1, 1009}} {
		t.Run(tc.name, func(t *testing.T) {
			gone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				defer close(gone)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			a, server := testApp(t, func(c *config) { c.upstream = "ws" + strings.TrimPrefix(upstream.URL, "http") })
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", http.Header{"Origin": {server.URL}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.WriteMessage(tc.kind, bytes.Repeat([]byte("x"), tc.size))
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err = conn.ReadMessage()
			if !websocket.IsCloseError(err, tc.code) {
				t.Fatalf("wrong close: %v", err)
			}
			select {
			case <-gone:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream survived downstream close")
			}
			a.close()
		})
	}
}
func TestProxyUpstreamClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1008, "policy"), time.Now().Add(time.Second))
	}))
	defer upstream.Close()
	_, server := testApp(t, func(c *config) { c.upstream = "ws" + strings.TrimPrefix(upstream.URL, "http") })
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, 1008) {
		t.Fatalf("upstream close changed: %v", err)
	}
}
