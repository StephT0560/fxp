package transport

import (
	"fmt"
	"io"
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
var clientSubs sync.Map // symbol -> *sync.Map[clientConn -> true]
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

		switch c := conn.(type) {
		case net.Conn:
			SendOrderToRust(c, raw)
		case *websocket.Conn:
			ForwardToTCPServer(c, raw)
		default:
			log.Println("❌ Unknown connection type in HandleFXPMessage for NewOrder")
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
	log.Printf("🚀 ForwardMarketData called with symbol: %s", symbol)
	log.Printf("📦 Forwarding market data for %s (%d bytes)", symbol, len(data))
	

	if clients, ok := clientSubs.Load(symbol); ok {
		count := 0
		clients.(*sync.Map).Range(func(key, _ interface{}) bool {
			count++
			var sendErr error
			switch conn := key.(type) {
			case net.Conn:
				_, sendErr = conn.Write(data)
			case *websocket.Conn:
				sendErr = conn.WriteMessage(websocket.BinaryMessage, data)
			default:
				log.Println("❌ Unknown client type for market data forward")
				return true
			}

			if sendErr != nil {
				log.Printf("❌ Failed to send market data to client (%T): %v", key, sendErr)
				closeConn(conn)
				clients.(*sync.Map).Delete(conn)
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


// Registers a TCP client for market data updates
func SubscribeToMarketDataTCP(clientConn net.Conn, request *protobuf.MarketDataRequest) {
	for _, symbol := range request.Symbols {
		clients, _ := clientSubs.LoadOrStore(symbol, &sync.Map{})
		clients.(*sync.Map).Store(clientConn, true)
	}
	log.Printf("✅ TCP Client subscribed to market data: %v", request.Symbols)

	// Debug log: how many clients are now subscribed to that symbol
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
}

// Registers a WebSocket client for market data updates
func SubscribeToMarketDataWS(clientConn *websocket.Conn, request *protobuf.MarketDataRequest) {
	for _, symbol := range request.Symbols {
		clients, _ := clientSubs.LoadOrStore(symbol, &sync.Map{})
		clients.(*sync.Map).Store(clientConn, true)
	}
	log.Printf("✅ WebSocket Client subscribed to market data: %v", request.Symbols)
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


// =================================================================
// ✅ WebSocket-specific: forward order to FXP Core and relay reply
// =================================================================
func ForwardToTCPServer(clientConn *websocket.Conn, fxpData []byte) {
	tcpConn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		log.Printf("❌ Failed to connect to FXP Core: %v", err)
		return
	}
	defer tcpConn.Close()

	_, err = tcpConn.Write(fxpData)
	if err != nil {
		log.Printf("❌ Failed to send FXPMessage to FXP Core: %v", err)
		return
	}
	log.Println("📤 FXPMessage forwarded to FXP Core")

	buffer := make([]byte, 1024)
	n, err := tcpConn.Read(buffer)
	if err != nil {
		if err == io.EOF {
			log.Println("⚠️ FXP Core closed the connection.")
		} else {
			log.Printf("❌ Failed to receive execution report from FXP Core: %v", err)
		}
		return
	}

	// Relay execution report back to WebSocket client
	err = clientConn.WriteMessage(websocket.BinaryMessage, buffer[:n])
	if err != nil {
		log.Printf("❌ Failed to forward execution report to WebSocket client: %v", err)
	} else {
		log.Println("📩 Execution report successfully forwarded to WebSocket client.")
	}
}

func encodeLengthPrefix(length int) []byte {
	prefix := make([]byte, 4)
	prefix[0] = byte(length >> 24)
	prefix[1] = byte(length >> 16)
	prefix[2] = byte(length >> 8)
	prefix[3] = byte(length)
	return prefix
}
