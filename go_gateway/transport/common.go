// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"fmt"
	"log"
	"log/slog"
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
	case *protobuf.MarketDataSnapshot:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_MarketDataSnapshot{MarketDataSnapshot: v},
		})
	case *protobuf.TradeCaptureReport:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_TradeCaptureReport{TradeCaptureReport: v},
		})
	case *protobuf.TradingSessionStatus:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_TradingSessionStatus{TradingSessionStatus: v},
		})
	case *protobuf.AuctionInfo:
		return proto.Marshal(&protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_AuctionInfo{AuctionInfo: v},
		})
	default:
		return nil, fmt.Errorf("unsupported FXP payload type: %T", payload)
	}
}

// ===================================================================
// ✅ Unified FXP Message Router (TCP or WebSocket, based on conn type)
// ===================================================================
func HandleFXPMessage(conn any, msg *protobuf.FXPMessage) {
	slog.Debug("HandleFXPMessage", "type", fmt.Sprintf("%T", msg.Payload))

	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_MarketDataRequest:
		slog.Debug("processing MarketDataRequest")
		switch c := conn.(type) {
		case net.Conn:
			SubscribeToMarketDataTCP(c, payload.MarketDataRequest)
		case *websocket.Conn:
			SubscribeToMarketDataWS(c, payload.MarketDataRequest)
		default:
			log.Println("❌ Unknown connection type in HandleFXPMessage for MarketDataRequest")
		}

	case *protobuf.FXPMessage_NewOrder:
		slog.Debug("processing NewOrder")
		if reason := validateOrderEnums(msg); reason != "" {
			sendOrderReject(conn, msg, reason)
			return
		}
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
		slog.Debug("processing OrderCancelRequest", "order_id", payload.OrderCancel.OrderId)
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
		slog.Debug("processing OrderCancelReplaceRequest", "order_id", cr.OrderId, "new_order_id", cr.NewOrderId)
		if reason := validateOrderEnums(msg); reason != "" {
			sendOrderReject(conn, msg, reason)
			return
		}
		raw, err := proto.Marshal(msg)
		if err != nil {
			log.Println("❌ Failed to encode cancel/replace request:", err)
			return
		}
		// Register the replacement ID before sending (so its EXEC_NEW is
		// routable) without overwriting an existing route; a rejected replace
		// drops it again via pendingReplace.
		if cr.NewOrderId != "" && registerRoute(cr.NewOrderId, orderRoute{conn: conn, owner: cr.Sender, provisional: true}) {
			pendingReplace.Store(cr.OrderId, cr.NewOrderId)
		}
		switch c := conn.(type) {
		case net.Conn:
			SendCancelToRust(c, raw, cr.OrderId)
		case *websocket.Conn:
			SendCancelWSToRust(c, raw, cr.OrderId)
		default:
			log.Println("❌ Unknown connection type for OrderCancelReplaceRequest")
		}

	case *protobuf.FXPMessage_OrderMassCancel:
		req := payload.OrderMassCancel
		raw, err := proto.Marshal(msg)
		if err != nil {
			log.Println("❌ Failed to encode mass cancel request:", err)
			return
		}
		// The core validates the request and always answers with a report.
		pushMassCancelRoute(req.Sender, req.ClientRequestId, conn)
		if err := writeToCore(frameBytes(raw)); err != nil {
			popMassCancelRoute(req.Sender, req.ClientRequestId)
			reject := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{
				OrderMassCancelReport: &protobuf.OrderMassCancelReport{
					ClientRequestId: req.ClientRequestId, Sender: req.Sender,
					Symbol: req.Symbol, Side: req.Side, Reason: req.Reason,
					RejectReason: "core unavailable",
				},
			}}
			if err := writeFXPMessage(conn, reject); err != nil {
				log.Printf("❌ Failed to send mass cancel reject: %v", err)
			}
		}

	case *protobuf.FXPMessage_MarketDataIncremental:
		slog.Debug("processing MarketDataIncrementalUpdate", "symbol", payload.MarketDataIncremental.Symbol)
		ForwardMarketData(payload.MarketDataIncremental)

	case *protobuf.FXPMessage_MarketDataSnapshot:
		ForwardMarketData(payload.MarketDataSnapshot)

	case *protobuf.FXPMessage_TradingSessionStatus:
		ForwardMarketData(payload.TradingSessionStatus)

	case *protobuf.FXPMessage_AuctionInfo:
		ForwardMarketData(payload.AuctionInfo)

	case *protobuf.FXPMessage_TradeCaptureReport:
		slog.Debug("processing TradeCaptureReport", "symbol", payload.TradeCaptureReport.Symbol)
		ForwardMarketData(payload.TradeCaptureReport)

	default:
		log.Printf("❌ Unhandled FXP payload type: %T", payload)
	}
}

func validateOrderEnums(msg *protobuf.FXPMessage) string {
	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder:
		if payload.NewOrder == nil {
			return "order is required"
		}
		order := payload.NewOrder
		if reason := validateEnum(int32(order.Side), "side", protobuf.OrderSide_name); reason != "" {
			return reason
		}
		if reason := validateEnum(int32(order.OrderType), "order_type", protobuf.OrderType_name); reason != "" {
			return reason
		}
		return validateEnum(int32(order.TimeInForce), "time_in_force", protobuf.TimeInForce_name)
	case *protobuf.FXPMessage_OrderCancelReplace:
		if payload.OrderCancelReplace == nil {
			return "order_cancel_replace is required"
		}
		value := int32(payload.OrderCancelReplace.NewTimeInForce)
		if value == 0 {
			return ""
		}
		if _, ok := protobuf.TimeInForce_name[value]; !ok {
			return fmt.Sprintf("unknown new_time_in_force %d", value)
		}
	}
	return ""
}

func validateEnum(value int32, name string, known map[int32]string) string {
	if value == 0 {
		return name + " is required"
	}
	if _, ok := known[value]; !ok {
		return fmt.Sprintf("unknown %s %d", name, value)
	}
	return ""
}

func sendOrderReject(conn any, msg *protobuf.FXPMessage, reason string) {
	var orderID, symbol string
	var side protobuf.OrderSide
	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder:
		if payload.NewOrder == nil {
			return
		}
		orderID = payload.NewOrder.OrderId
		symbol = payload.NewOrder.Symbol
		side = payload.NewOrder.Side
	case *protobuf.FXPMessage_OrderCancelReplace:
		if payload.OrderCancelReplace == nil {
			return
		}
		orderID = payload.OrderCancelReplace.NewOrderId
		symbol = payload.OrderCancelReplace.Symbol
	default:
		return
	}
	reject := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_ExecutionReport{
			ExecutionReport: &protobuf.ExecutionReport{
				OrderId: orderID, Symbol: symbol, Side: side,
				ExecutionType: protobuf.ExecutionType_EXEC_REJECTED,
				OrderStatus:   protobuf.OrderStatus_ORDER_REJECTED,
				RejectReason:  reason,
			},
		},
	}
	if err := writeFXPMessage(conn, reject); err != nil {
		log.Printf("❌ Failed to send invalid-order reject: %v", err)
	}
}

func writeFXPMessage(conn any, msg *protobuf.FXPMessage) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	switch c := conn.(type) {
	case net.Conn:
		_, err = c.Write(frame)
	case *websocket.Conn:
		err = c.WriteMessage(websocket.BinaryMessage, raw)
	default:
		return fmt.Errorf("unsupported connection type: %T", conn)
	}
	return err
}

// ==================================================================
// ✅ Forward Market Data to TCP or WebSocket clients by symbol
// ==================================================================
func ForwardMarketData(update proto.Message) {
	var symbol string
	switch msg := update.(type) {
	case *protobuf.MarketDataIncrementalUpdate:
		symbol = msg.Symbol
	case *protobuf.MarketDataSnapshot:
		symbol = msg.Symbol
	case *protobuf.TradeCaptureReport:
		symbol = msg.Symbol
	case *protobuf.TradingSessionStatus:
		symbol = msg.Symbol
	case *protobuf.AuctionInfo:
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

	slog.Debug("forwarding market data", "symbol", symbol, "bytes", len(data))

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
				slog.Debug("market data sent", "client", fmt.Sprintf("%T", key), "symbol", symbol)
			}
			return true
		})
		slog.Debug("market data forwarding complete", "clients", count, "symbol", symbol)
	} else {
		slog.Debug("no clients subscribed", "symbol", symbol)
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
			slog.Debug("clients subscribed", "count", count, "symbol", symbol)
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
