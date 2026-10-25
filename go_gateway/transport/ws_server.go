// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go_gateway/protobuf"
	"go_gateway/tlsconfig"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: checkOrigin}

// checkOrigin admits a WebSocket handshake when it has no Origin header
// (non-browser clients), is same-origin (Origin scheme and host equal the
// request's), or its Origin is listed in FXP_GW_WS_ALLOWED_ORIGINS
// (comma-separated, e.g. "https://trader.example,http://ops.example:8443").
// Every other browser origin is rejected with 403.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		log.Printf("⛔ WebSocket handshake with malformed Origin %q rejected", origin)
		return false
	}
	if strings.EqualFold(u.Host, r.Host) && strings.EqualFold(u.Scheme, requestScheme(r)) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("FXP_GW_WS_ALLOWED_ORIGINS"), ",") {
		if a := strings.TrimSpace(allowed); a != "" && strings.EqualFold(strings.TrimSuffix(a, "/"), origin) {
			return true
		}
	}
	log.Printf("⛔ WebSocket handshake from origin %q rejected (not same-origin, not in FXP_GW_WS_ALLOWED_ORIGINS)", origin)
	return false
}

func StartWebSocketServer() {
	if err := RunWebSocketServer(context.Background()); err != nil {
		log.Fatalf("❌ WebSocket server stopped: %v", err)
	}
}

func RunWebSocketServer(ctx context.Context) error {
	manager, err := tlsconfig.ServerFromEnv()
	if err != nil {
		return err
	}
	go manager.Watch(ctx, time.Second)
	return RunWebSocketServerWithTLS(ctx, manager)
}
func RunWebSocketServerWithTLS(ctx context.Context, manager *tlsconfig.Server) error {
	addr := os.Getenv("FXP_GW_WS_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	listener, err := manager.Listen(addr)
	if err != nil {
		return err
	}
	return runWebSocketServer(ctx, listener, shutdownTimeout())
}

func runWebSocketServer(ctx context.Context, listener net.Listener, timeout time.Duration) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleConnections)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: currentLimits().HandshakeTimeout}
	log.Printf("WebSocket server listening on %s", listener.Addr())

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.Serve(listener) }()
	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownClientWriters(timeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return err
		}
		err := <-serverErr
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	lim, conns := currentLimits(), wsConnsP.Load()
	if !conns.tryAcquire() {
		log.Printf("⛔ WebSocket connection limit (%d) reached — rejecting %s", lim.MaxConns, r.RemoteAddr)
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer conns.release()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ WebSocket upgrade failed:", err)
		return
	}
	registerClientWriter(conn)
	defer unregisterClientWriter(conn)
	conn.SetReadLimit(int64(lim.MaxFramePreAuth))
	_ = conn.SetReadDeadline(time.Now().Add(lim.HandshakeTimeout))
	bucket := newTokenBucket(lim.RatePerSec, lim.RateBurst)
	authed := false

	log.Println("📡 New WebSocket client connected")

	// sessionID is empty until a valid Logon is received for this connection.
	sessionID := ""
	defer func() {
		if sessionID != "" {
			endSession(sessionID)
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("❌ WebSocket client disconnected:", err)
			break
		}

		if !bucket.allow(time.Now()) {
			sendSessionError(conn, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "message rate limit exceeded")
			continue
		}

		msg, err := DecodeFXPMessage(message)
		if err != nil {
			log.Println("❌ Failed to decode FXPMessage from WebSocket client:", err)
			continue
		}

		// Auth-aware router: enforces Logon before any other message type.
		HandleFXPMessageAuthenticated(conn, msg, &sessionID)
		if !authed && sessionID != "" {
			authed = true
			conn.SetReadLimit(int64(lim.MaxFrame))
			_ = conn.SetReadDeadline(time.Time{})
		}
	}
}

// requestScheme is the scheme the browser used to reach the gateway: https if
// TLS terminated here, otherwise the first X-Forwarded-Proto hop set by a
// TLS-terminating proxy, otherwise http.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp != "" {
		return strings.TrimSpace(strings.Split(xfp, ",")[0])
	}
	return "http"
}
