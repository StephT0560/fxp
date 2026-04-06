package transport

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// StartWebSocketServer initializes the WebSocket server
func StartWebSocketServer() {
	http.HandleFunc("/ws", handleConnections)
	log.Println("✅ WebSocket Server started on ws://localhost:8081")

	if err := http.ListenAndServe(":8081", nil); err != nil {
		log.Fatalf("❌ WebSocket server error: %v", err)
	}
}

// handleConnections manages new WebSocket connections
func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ WebSocket upgrade failed:", err)
		return
	}

	log.Println("📡 New WebSocket client connected")

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("❌ WebSocket client disconnected:", err)
			break
		}

		msg, err := DecodeFXPMessage(message)
		if err != nil {
			log.Println("❌ Failed to decode FXPMessage from WebSocket client:", err)
			continue
		}

		HandleFXPMessage(conn, msg)
	}
}
