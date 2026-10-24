package transport

import (
	"encoding/binary"
	"errors"
	"log"
	"net"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func StartTCPServer() {
	if err := InitPool(); err != nil {
		log.Fatalf("❌ Failed to initialise FXP Core connection pool: %v", err)
	}
	listener, err := net.Listen("tcp", ":8082")
	if err != nil {
		log.Fatalf("❌ Failed to start TCP server: %v", err)
	}
	defer listener.Close()
	log.Println("✅ TCP Server started on 127.0.0.1:8082")
	serveTCP(listener)
}

func serveTCP(listener net.Listener) {
	lim, conns := currentLimits(), tcpConnsP.Load()
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Println("❌ Failed to accept TCP connection:", err)
			continue
		}
		if !conns.tryAcquire() {
			log.Printf("⛔ TCP connection limit (%d) reached — rejecting %s", lim.MaxConns, clientConn.RemoteAddr())
			clientConn.Close()
			continue
		}
		go func() {
			defer conns.release()
			handleTCPClient(clientConn)
		}()
	}
}

func handleTCPClient(clientConn net.Conn) {
	defer clientConn.Close()
	lim := currentLimits()
	sessionID := ""
	defer func() {
		if sessionID != "" {
			invalidateSession(sessionID)
		}
	}()
	bucket := newTokenBucket(lim.RatePerSec, lim.RateBurst)
	authed := false
	// Logon must complete within the handshake timeout.
	_ = clientConn.SetReadDeadline(time.Now().Add(lim.HandshakeTimeout))
	for {
		maxFrame := lim.MaxFramePreAuth
		if authed {
			maxFrame = lim.MaxFrame
		}
		msgBuf, err := readFrame(clientConn, maxFrame)
		if err != nil {
			log.Printf("❌ Closing TCP client: %v", err)
			return
		}
		if !bucket.allow(time.Now()) {
			sendSessionError(clientConn, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "message rate limit exceeded")
			continue
		}
		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(msgBuf, msg); err != nil {
			log.Println("❌ Invalid FXPMessage from TCP client:", err)
			continue
		}
		if msg.Payload == nil {
			log.Println("⚠️ FXPMessage has no payload")
			continue
		}
		HandleFXPMessageAuthenticated(clientConn, msg, &sessionID)
		if !authed && sessionID != "" {
			authed = true
			_ = clientConn.SetReadDeadline(time.Time{})
		}
	}
}

func SendOrderToRust(clientConn net.Conn, orderData []byte) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(orderData, msg); err != nil {
		log.Printf("❌ SendOrderToRust: failed to decode order: %v", err)
		return
	}
	order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder)
	if !ok {
		log.Println("❌ SendOrderToRust: payload is not a NewOrder")
		return
	}
	orderID := order.NewOrder.OrderId
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendOrderToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	log.Printf("🧠 Registered OrderID %s to TCP clientConn", orderID)
	SendFramedToCore(clientConn, frame, orderID, order.NewOrder.Sender, false)
}

func ForwardToTCPServer(wsConn *websocket.Conn, msg *protobuf.FXPMessage, frame []byte) {
	order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder)
	if !ok {
		log.Println("❌ ForwardToTCPServer: payload is not a NewOrder")
		return
	}
	orderID := order.NewOrder.OrderId
	log.Printf("🧠 Registered WS OrderID %s to wsConn", orderID)
	SendFramedToCore(wsConn, frame, orderID, order.NewOrder.Sender, false)
	log.Println("📤 WS order forwarded to FXP Core via pool connection")
}

func forwardSubscriptionToCore(request *protobuf.MarketDataRequest) {
	// Filter out empty symbol strings — Rust would spawn a ghost order book task for them
	filtered := make([]string, 0, len(request.Symbols))
	for _, s := range request.Symbols {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return
	}

	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{
			MarketDataRequest: &protobuf.MarketDataRequest{
				Symbols: filtered,
			},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ Failed to encode MarketDataRequest for Rust Core: %v", err)
		return
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	pc := acquire()
	defer release(pc)
	if err := pc.write(frame); err != nil {
		log.Printf("❌ Failed to send MarketDataRequest to Rust Core: %v", err)
		return
	}
	log.Printf("📡 MarketDataRequest forwarded to Rust Core for symbols: %v", filtered)
}

// SendCancelToRust forwards an OrderCancelRequest or OrderCancelReplaceRequest
// to Rust Core via the connection pool. The original order_id is registered in
// orderClientMap so the resulting ExecutionReport is routed back correctly.
func SendCancelToRust(clientConn net.Conn, rawMsg []byte, orderID string) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(rawMsg, msg); err != nil {
		log.Printf("❌ SendCancelToRust: decode failed: %v", err)
		return
	}
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendCancelToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	log.Printf("🗑️ Sending cancel for OrderID %s to Rust Core", orderID)
	SendFramedToCore(clientConn, frame, orderID, orderOwner(msg), true)
}

// SendCancelWSToRust forwards a cancel/replace from a WebSocket client.
func SendCancelWSToRust(wsConn *websocket.Conn, rawMsg []byte, orderID string) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(rawMsg, msg); err != nil {
		log.Printf("❌ SendCancelWSToRust: decode failed: %v", err)
		return
	}
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendCancelWSToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	log.Printf("🗑️ Sending WS cancel for OrderID %s to Rust Core", orderID)
	SendFramedToCore(wsConn, frame, orderID, orderOwner(msg), true)
}