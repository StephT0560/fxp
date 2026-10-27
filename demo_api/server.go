// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go_gateway/symbolconfig"
	"go_gateway/test_clients/devjwt"

	"github.com/gorilla/websocket"
)

var demoUsers = []string{"alice", "bob", "market-maker"}
var defaultSymbols = []string{"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA", "TSLA", "META", "JPM", "ETH/USD", "BTC/USD"}

const maxWSMessage = 1 << 20

type rateEntry struct {
	start time.Time
	count int
}
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
	limit   int
}

func (l *rateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]rateEntry)
	}
	e, exists := l.entries[ip]
	if !exists || now.Sub(e.start) >= time.Minute {
		if !exists && len(l.entries) >= 4096 {
			for k, v := range l.entries {
				if now.Sub(v.start) >= time.Minute {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= 4096 {
				return false
			}
		}
		e = rateEntry{start: now}
	}
	if e.count >= l.limit {
		return false
	}
	e.count++
	l.entries[ip] = e
	return true
}

type symbolView struct {
	Symbol      string `json:"symbol"`
	PriceScale  int    `json:"price_scale"`
	TickSize    string `json:"tick_size"`
	LotSize     uint32 `json:"lot_size"`
	MinQuantity uint32 `json:"min_quantity"`
	MaxQuantity uint32 `json:"max_quantity"`
}
type app struct {
	activityMu sync.Mutex
	scenarios  scenarioManager
	cfg        config
	originHost string
	symbols    []symbolView
	sim        simulator
	limiter    rateLimiter
	mux        *http.ServeMux
	mu         sync.Mutex
	closed     bool
	sockets    map[*websocket.Conn]struct{}
	openCall   func(ctx context.Context, seconds int) error
}

func newApp(cfg config) (*app, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	rules := symbolconfig.Config{}
	if cfg.symbolFile != "" {
		data, err := os.ReadFile(cfg.symbolFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read FXP_SYMBOL_CONFIG")
		}
		rules, err = symbolconfig.Parse(string(data))
		if err != nil {
			return nil, err
		}
	}
	origin, _ := url.Parse(cfg.origin)
	a := &app{cfg: cfg, originHost: origin.Host, sim: simulator{cfg: cfg}, limiter: rateLimiter{limit: cfg.tokenRate}, sockets: make(map[*websocket.Conn]struct{}), mux: http.NewServeMux(), openCall: cfg.openCall}
	names := make(map[string]bool)
	for _, name := range defaultSymbols {
		names[name] = true
	}
	for name := range rules {
		names[name] = true
	}
	for name := range names {
		r := rules.For(name)
		a.symbols = append(a.symbols, symbolView{name, r.PriceScale, fmt.Sprint(r.TickSize), r.LotSize, r.MinQuantity, r.MaxQuantity})
	}
	sort.Slice(a.symbols, func(i, j int) bool { return a.symbols[i].Symbol < a.symbols[j].Symbol })
	a.mux.HandleFunc("GET /api/scenarios", a.scenariosList)
	a.mux.HandleFunc("POST /api/scenarios/{id}/run", a.scenarioStart)
	a.mux.HandleFunc("POST /api/scenarios/{id}/stop", a.scenarioStop)
	a.mux.HandleFunc("GET /api/scenarios/{id}/events", a.scenarioEvents)
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	a.mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, demoUsers) })
	a.mux.HandleFunc("GET /api/symbols", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.symbols) })
	a.mux.HandleFunc("GET /api/bench", a.bench)
	a.mux.HandleFunc("GET /api/bench/profile-r4", a.benchProfile)
	a.mux.HandleFunc("POST /api/inspect/fix", a.inspectFIX)
	a.mux.HandleFunc("POST /api/token", a.token)
	a.mux.HandleFunc("POST /api/sim/start", a.simStart)
	a.mux.HandleFunc("POST /api/sim/stop", func(w http.ResponseWriter, r *http.Request) {
		a.sim.stop(false)
		writeJSON(w, http.StatusOK, a.sim.status())
	})
	a.mux.HandleFunc("GET /api/sim/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.sim.status()) })
	a.mux.HandleFunc("GET /ws", a.proxy)
	a.mux.HandleFunc("GET /", a.static)
	return a, nil
}
func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self' "+strings.Replace(a.cfg.origin, "http://", "ws://", 1)+"; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	// Pin the configured public host as well as Origin to avoid DNS rebinding.
	if r.Host != a.originHost {
		http.Error(w, "unrecognized host", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost && ((r.Header.Get("Origin") != "" && !a.sameOrigin(r)) || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
		http.Error(w, "foreign origin", http.StatusForbidden)
		return
	}
	a.mux.ServeHTTP(w, r)
}
func (a *app) sameOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	return len(values) == 1 && values[0] == a.cfg.origin
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "expected one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}
func (a *app) token(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "invalid peer address", http.StatusBadRequest)
		return
	}
	if !a.limiter.allow(ip, time.Now()) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "token rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var req struct {
		User string `json:"user"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	valid := false
	for _, u := range demoUsers {
		if req.User == u {
			valid = true
		}
	}
	if !valid {
		http.Error(w, "unknown demo user", http.StatusBadRequest)
		return
	}
	token, err := devjwt.GenerateWithTTL(a.cfg.secret, "demo:"+req.User, a.cfg.tokenTTL)
	if err != nil {
		http.Error(w, "token unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_in": int(a.cfg.tokenTTL / time.Second)})
}
func (a *app) simStart(w http.ResponseWriter, r *http.Request) {
	a.activityMu.Lock()
	defer a.activityMu.Unlock()
	a.scenarios.mu.Lock()
	active := a.scenarios.run
	a.scenarios.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		default:
			http.Error(w, "stop the scenario before starting the simulator", http.StatusConflict)
			return
		}
	}
	var p simParams
	if !decodeJSON(w, r, &p) {
		return
	}
	if err := p.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := a.sim.startRun(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// The simulator connects first and waits; the core's call starts only
	// then, so both auction clocks start together. Any failure stops the run.
	if p.OpeningAuctionSec > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		err := a.sim.waitReady(ctx, run)
		if err == nil {
			err = a.openCall(ctx, p.OpeningAuctionSec)
		}
		if err == nil {
			err = a.sim.release(run)
		}
		if err != nil {
			a.sim.stopRun(run)
			http.Error(w, "opening auction unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, a.sim.status())
}
func (a *app) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws/" {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(a.cfg.staticDir)
	if err == nil {
		if _, indexErr := root.Stat("index.html"); indexErr != nil {
			_ = root.Close()
			root = nil
			err = indexErr
		}
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>FXP Console</title><h1>FXP Console</h1><p>The local demo API is ready. The UI has not been built yet.</p></html>")
		return
	}
	defer func() { _ = root.Close() }()
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	file, err := root.Open(name)
	if err != nil {
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		file, err = root.Open("index.html")
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(file.Name()), stat.ModTime(), file)
}
func (a *app) close() {
	a.scenarios.close()
	a.mu.Lock()
	a.closed = true
	for conn := range a.sockets {
		_ = conn.Close()
	}
	a.mu.Unlock()
	a.sim.stop(true)
}
