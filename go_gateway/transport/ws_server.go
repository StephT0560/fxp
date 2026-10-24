// SPDX-License-Identifier: MIT

package transport

import (
	"log"
	"net/http"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
)

// CheckOrigin should be tightened in production to an allowlist of known origins.
// For local dev the wildcard is acceptable; remove it before any external deployment.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// TODO: replace with origin allowlist, e.g.:
		// allowed := map[string]bool{"https://yourdomain.com": true}
		// return allowed[r.Header.Get("Origin")]
		return true
	},
}

func StartWebSocketServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleConnections)
	srv := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: currentLimits().HandshakeTimeout}
	log.Println("✅ WebSocket Server started on ws://localhost:8081")

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("❌ WebSocket server error: %v", err)
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
	defer conn.Close()
	conn.SetReadLimit(int64(lim.MaxFramePreAuth))
	_ = conn.SetReadDeadline(time.Now().Add(lim.HandshakeTimeout))
	bucket := newTokenBucket(lim.RatePerSec, lim.RateBurst)
	authed := false

	log.Println("📡 New WebSocket client connected")

	// sessionID is empty until a valid Logon is received for this connection.
	sessionID := ""
	defer func() {
		if sessionID != "" {
			invalidateSession(sessionID)
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