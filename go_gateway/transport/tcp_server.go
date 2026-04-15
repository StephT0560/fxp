package transport

import (
	"encoding/binary"
	"io"
	"log"
	"net"

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
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Println("❌ Failed to accept TCP connection:", err)
			continue
		}
		go handleTCPClient(clientConn)
	}
}

func handleTCPClient(clientConn net.Conn) {
	defer clientConn.Close()
	sessionID := ""
	for {
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(clientConn, lenBuf); err != nil {
			log.Printf("❌ Failed to read message length: %v", err)
			return
		}
		msgLen := binary.BigEndian.Uint32(lenBuf)
		msgBuf := make([]byte, msgLen)
		if _, err := io.ReadFull(clientConn, msgBuf); err != nil {
			log.Printf("❌ Failed to read full FXPMessage: %v", err)
			return
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
	SendFramedToCore(clientConn, frame, orderID)
}

func ForwardToTCPServer(wsConn *websocket.Conn, msg *protobuf.FXPMessage, frame []byte) {
	order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder)
	if !ok {
		log.Println("❌ ForwardToTCPServer: payload is not a NewOrder")
		return
	}
	orderID := order.NewOrder.OrderId
	log.Printf("🧠 Registered WS OrderID %s to wsConn", orderID)
	SendFramedToCore(wsConn, frame, orderID)
	log.Println("📤 WS order forwarded to FXP Core via pool connection")
}

func forwardSubscriptionToCore(request *protobuf.MarketDataRequest) {
	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: request},
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
	log.Printf("📡 MarketDataRequest forwarded to Rust Core for symbols: %v", request.Symbols)
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
	SendFramedToCore(clientConn, frame, orderID)
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
	SendFramedToCore(wsConn, frame, orderID)
}