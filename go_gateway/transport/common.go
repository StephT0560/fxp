package transport

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// ===============================
// ✅ Shared Client Subscriptions
// ===============================
var clientSubs sync.Map    // symbol -> *sync.Map[clientConn -> true]
var orderClientMap sync.Map

// ============================================
// ✅ FXP Message Decode Helper (from []byte)
// ============================================
func DecodeFXPMessage(data []byte) (*protobuf.FXPMessage, error) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("FXPMessage decode failed: %w", err)
	}
	return msg, nil
}

// ============================================
// ✅ FXP Message Encode Helper (from payload)
// ============================================
func EncodeFXPMessage(payload proto.Message) ([]byte, error) {
	switch v := payload.(type) {
	case *protobuf.NewOrderSingle:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{NewOrder: v},
		})
	case *protobuf.OrderCancelRequest:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_OrderCancel{OrderCancel: v},
		})
	case *protobuf.OrderCancelReplaceRequest:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_OrderCancelReplace{OrderCancelReplace: v},
		})
	case *protobuf.MarketDataRequest:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: v},
		})
	case *protobuf.ExecutionReport:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: v},
		})
	case *protobuf.MarketDataIncrementalUpdate:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_MarketDataIncremental{MarketDataIncremental: v},
		})
	case *protobuf.TradeCaptureReport:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_TradeCaptureReport{TradeCaptureReport: v},
		})
	default:
		return nil, fmt.Errorf("unsupported FXP payload type: %T", payload)
	}
}

// ===================================================================
// ✅ Unified FXP Message Router (TCP or WebSocket, based on conn type)
// ===================================================================
func HandleFXPMessage(conn any, msg *protobuf.FXPMessage) {
	log.Printf("🔎 HandleFXPMessage called with type: %T", msg.Payload)

	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_MarketDataRequest:
		log.Println("➡️ Processing MarketDataRequest")
		switch c := conn.(type) {
		case net.Conn:
			SubscribeToMarketDataTCP(c, payload.MarketDataRequest)
		case *websocket.Conn:
			SubscribeToMarketDataWS(c, payload.MarketDataRequest)
		default:
			log.Println("❌ Unknown connection type in HandleFXPMessage for MarketDataRequest")
		}

	case *protobuf.FXPMessage_NewOrder:
		log.Println("➡️ Processing NewOrder")
		raw, err := proto.Marshal(msg)
		if err != nil {
			log.Println("❌ Failed to encode order:", err)
			return
		}

		// Frame the message with a 4-byte length prefix
		frame := make([]byte, 4+len(raw))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
		copy(frame[4:], raw)

		switch c := conn.(type) {
		case net.Conn:
			SendOrderToRust(c, raw) // raw body only — SendOrderToRust frames internally
		case *websocket.Conn:
			ForwardToTCPServer(c, msg, frame) // pool-based, no throwaway connections
		default:
			log.Println("❌ Unknown connection type in HandleFXPMessage for NewOrder")
		}

	case *protobuf.FXPMessage_OrderCancel:
		log.Printf("➡️ Processing OrderCancelRequest for OrderID %s", payload.OrderCancel.OrderId)
		raw, err := proto.Marshal(msg)
		if err != nil {
			log.Println("❌ Failed to encode cancel request:", err)
			return
		}
		switch c := conn.(type) {
		case net.Conn:
			SendCancelToRust(c, raw, payload.OrderCancel.OrderId)
		case *websocket.Conn:
			SendCancelWSToRust(c, raw, payload.OrderCancel.OrderId)
		default:
			log.Println("❌ Unknown connection type for OrderCancelRequest")
		}

	case *protobuf.FXPMessage_OrderCancelReplace:
		cr := payload.OrderCancelReplace
		log.Printf("➡️ Processing OrderCancelReplaceRequest for OrderID %s → %s", cr.OrderId, cr.NewOrderId)
		raw, err := proto.Marshal(msg)
		if err != nil {
			log.Println("❌ Failed to encode cancel/replace request:", err)
			return
		}
		switch c := conn.(type) {
		case net.Conn:
			// Register both the original order ID and the new order ID so that
			// EXEC_REPLACED (for original) and EXEC_NEW (for replacement) are
			// both routed back to this client.
			SendCancelToRust(c, raw, cr.OrderId)
			if cr.NewOrderId != "" {
				orderClientMap.Store(cr.NewOrderId, c)
				log.Printf("🧠 Registered replacement OrderID %s to TCP clientConn", cr.NewOrderId)
			}
		case *websocket.Conn:
			SendCancelWSToRust(c, raw, cr.OrderId)
			if cr.NewOrderId != "" {
				orderClientMap.Store(cr.NewOrderId, c)
				log.Printf("🧠 Registered replacement OrderID %s to WS conn", cr.NewOrderId)
			}
		default:
			log.Println("❌ Unknown connection type for OrderCancelReplaceRequest")
		}

	case *protobuf.FXPMessage_MarketDataIncremental:
		log.Println("➡️ Processing MarketDataIncrementalUpdate")
		log.Printf("📡 MarketDataIncrementalUpdate received for symbol: %s", payload.MarketDataIncremental.Symbol)
		ForwardMarketData(payload.MarketDataIncremental)

	case *protobuf.FXPMessage_TradeCaptureReport:
		log.Println("➡️ Processing TradeCaptureReport")
		log.Printf("📡 TradeCaptureReport received for symbol: %s", payload.TradeCaptureReport.Symbol)
		ForwardMarketData(payload.TradeCaptureReport)

	default:
		log.Printf("❌ Unhandled FXP payload type: %T", payload)
	}
}

// ==================================================================
// ✅ Forward Market Data to TCP or WebSocket clients by symbol
// ==================================================================
func ForwardMarketData(update proto.Message) {
	var symbol string
	switch msg := update.(type) {
	case *protobuf.MarketDataIncrementalUpdate:
		symbol = msg.Symbol
	case *protobuf.TradeCaptureReport:
		symbol = msg.Symbol
	default:
		log.Println("❌ Unknown update type for forwarding")
		return
	}

	data, err := EncodeFXPMessage(update)
	if err != nil {
		log.Printf("❌ Failed to encode market data for symbol %s: %v", symbol, err)
		return
	}

	// Frame with length prefix for TCP clients
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)

	log.Printf("🚀 ForwardMarketData called with symbol: %s", symbol)
	log.Printf("📦 Forwarding market data for %s (%d bytes)", symbol, len(data))

	if clients, ok := clientSubs.Load(symbol); ok {
		count := 0
		clients.(*sync.Map).Range(func(key, _ interface{}) bool {
			count++
			var sendErr error
			switch c := key.(type) {
			case net.Conn:
				// TCP clients expect length-prefixed frames
				_, sendErr = c.Write(frame)
			case *websocket.Conn:
				// WebSocket clients get raw Protobuf bytes (WS has its own framing)
				sendErr = c.WriteMessage(websocket.BinaryMessage, data)
			default:
				log.Println("❌ Unknown client type for market data forward")
				return true
			}

			if sendErr != nil {
				log.Printf("❌ Failed to send market data to client (%T): %v", key, sendErr)
				closeConn(key)
				clients.(*sync.Map).Delete(key)
			} else {
				log.Printf("✅ Market data sent to client (%T) for symbol: %s", key, symbol)
			}
			return true
		})
		log.Printf("📊 Forwarding complete: %d clients attempted for %s", count, symbol)
	} else {
		log.Printf("🚫 No clients subscribed for symbol: %s", symbol)
	}
}

// Registers a TCP client for market data updates and forwards the
// subscription to Rust Core so it starts broadcasting on this symbol.
func SubscribeToMarketDataTCP(clientConn net.Conn, request *protobuf.MarketDataRequest) {
	for _, symbol := range request.Symbols {
		clients, _ := clientSubs.LoadOrStore(symbol, &sync.Map{})
		clients.(*sync.Map).Store(clientConn, true)
	}
	log.Printf("✅ TCP Client subscribed to market data: %v", request.Symbols)

	for _, symbol := range request.Symbols {
		if subs, ok := clientSubs.Load(symbol); ok {
			count := 0
			subs.(*sync.Map).Range(func(_, _ any) bool {
				count++
				return true
			})
			log.Printf("🔁 %d clients subscribed to %s", count, symbol)
		}
	}

	// Forward subscription to Rust Core so it starts emitting incremental
	// updates for this symbol on the persistent connection.
	forwardSubscriptionToCore(request)
}

// Registers a WebSocket client for market data updates and forwards the
// subscription to Rust Core.
func SubscribeToMarketDataWS(clientConn *websocket.Conn, request *protobuf.MarketDataRequest) {
	for _, symbol := range request.Symbols {
		clients, _ := clientSubs.LoadOrStore(symbol, &sync.Map{})
		clients.(*sync.Map).Store(clientConn, true)
	}
	log.Printf("✅ WebSocket Client subscribed to market data: %v", request.Symbols)

	forwardSubscriptionToCore(request)
}

// ====================================================
// ✅ Graceful connection close for TCP or WebSocket
// ====================================================
func closeConn(conn interface{}) {
	switch c := conn.(type) {
	case net.Conn:
		log.Println("⚠️ Closing dead TCP client connection")
		_ = c.Close()
	case *websocket.Conn:
		log.Println("⚠️ Closing dead WebSocket client connection")
		_ = c.Close()
	default:
		log.Println("⚠️ Unknown connection type for close")
	}
}

// ForwardToTCPServer and ForwardToTCPServer are defined in tcp_server.go.
// They use the connection pool (pool.go) instead of the old single conn.