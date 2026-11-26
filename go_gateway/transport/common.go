// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"fmt"
	"go_gateway/metrics"
	"log"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

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
	metrics.Processed(msg)
	slog.Debug("HandleFXPMessage", "type", fmt.Sprintf("%T", msg.Payload))

	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_OrderStatusRequest:
		forwardStatusRequest(conn, msg)

	case *protobuf.FXPMessage_SessionControl:
		// HandleFXPMessageAuthenticated admits only operator sessions.
		forwardSessionControl(conn, msg)

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
		forwardMassCancelRequest(conn, msg)

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
	metrics.Response(msg)
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return enqueueClientData(conn, raw, false)
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

	if phase1Enabled {
		if _, ok := update.(*protobuf.MarketDataIncrementalUpdate); ok {
			phase1Count("encodes")
		}
	}

	data, err := EncodeFXPMessage(update)
	if err != nil {
		log.Printf("❌ Failed to encode market data for symbol %s: %v", symbol, err)
		return
	}

	slog.Debug("forwarding market data", "symbol", symbol, "bytes", len(data))

	if clients, ok := clientSubs.Load(symbol); ok {
		count := 0
		clients.(*sync.Map).Range(func(key, _ interface{}) bool {
			count++
			if sendErr := enqueueClientData(key, data, true); sendErr != nil {
				log.Printf("❌ Failed to send market data to client (%T): %v", key, sendErr)
				closeConn(key)
			}
			return true
		})
		slog.Debug("market data forwarding complete", "clients", count, "symbol", symbol)
	} else {
		slog.Debug("no clients subscribed", "symbol", symbol)
	}
}

const (
	maxSubscriptionsPerClient  = 256
	maxSubscriptionSymbols     = 4096
	maxSubscriptionSymbolBytes = 128
)

// Serializes membership changes and reconnect snapshots.
var subscriptionsMu sync.Mutex
var subscriptionsByClient = make(map[any]map[string]struct{})
var subscriptionRefCount = make(map[string]int)

func SubscribeToMarketDataTCP(conn net.Conn, request *protobuf.MarketDataRequest) {
	subscribeToMarketData(conn, request)
}

func SubscribeToMarketDataWS(conn *websocket.Conn, request *protobuf.MarketDataRequest) {
	subscribeToMarketData(conn, request)
}

func subscribeToMarketData(conn any, request *protobuf.MarketDataRequest) {
	if request.Unsubscribe {
		removeSubscriptionSymbols(conn, request.Symbols)
		return
	}
	symbols, ok := subscriptionSymbols(request.Symbols)
	if !ok {
		_ = writeFXPMessage(conn, marketDataReject(request.RequestId, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "market data subscription limit exceeded"))
		return
	}
	if len(symbols) == 0 {
		return
	}
	// Validate against existing Core books before charging the shared budget.
	// Per-connection handlers serialize admission with subsequent unsubscribe.
	result := make(chan bool, 1)
	r := &pendingRequest{conn: conn, original: request.RequestId, kind: "market data", marketDataResult: result,
		reject: func(reason string) *protobuf.FXPMessage {
			result <- false
			return marketDataReject(request.RequestId, protobuf.ErrorType_SYSTEM_ERROR, reason)
		},
	}
	key, err := registerPending(r, 30*time.Second)
	if err != nil {
		_ = writeFXPMessage(conn, marketDataReject(request.RequestId, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, err.Error()))
		return
	}
	raw, err := EncodeFXPMessage(&protobuf.MarketDataRequest{Symbols: symbols, RequestId: key, ValidateOnly: true})
	if err == nil {
		err = writeSubscription(frameBytes(raw))
	}
	if err != nil {
		if pending := pendingRequests.get(key, "", true, "market data"); pending != nil {
			_ = writeFXPMessage(conn, pending.reject("core unavailable"))
		}
	}
	select {
	case accepted := <-result:
		if !accepted {
			return
		}
	case <-r.writer.stopped:
		return
	case <-r.writer.draining:
		return
	}
	if _, ok := registerSubscriptions(conn, symbols); !ok {
		_ = writeFXPMessage(conn, marketDataReject(request.RequestId, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "market data subscription limit exceeded"))
		return
	}
	// A disconnect racing validation must not resurrect membership state.
	select {
	case <-r.writer.stopped:
		removeSubscriptions(conn)
		return
	default:
	}
	if request.RequestId != "" {
		_ = writeFXPMessage(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequestAck{
			MarketDataRequestAck: &protobuf.MarketDataRequestAck{RequestId: request.RequestId},
		}})
	}
	forwardSubscriptionToCore(&protobuf.MarketDataRequest{Symbols: symbols})
}

func subscriptionSymbols(requested []string) ([]string, bool) {
	symbols := make([]string, 0, min(len(requested), maxSubscriptionsPerClient))
	seen := make(map[string]struct{})
	for _, symbol := range requested {
		if symbol == "" {
			continue
		}
		if len(symbol) > maxSubscriptionSymbolBytes {
			return nil, false
		}
		if _, duplicate := seen[symbol]; duplicate {
			continue
		}
		seen[symbol] = struct{}{}
		symbols = append(symbols, symbol)
		if len(symbols) > maxSubscriptionsPerClient {
			return nil, false
		}
	}
	return symbols, true
}

func registerSubscriptions(conn any, requested []string) ([]string, bool) {
	symbols, ok := subscriptionSymbols(requested)
	if !ok {
		return nil, false
	}
	subscriptionsMu.Lock()
	defer subscriptionsMu.Unlock()
	existing := subscriptionsByClient[conn]
	newForClient, newGlobal := 0, 0
	for _, symbol := range symbols {
		if _, subscribed := existing[symbol]; !subscribed {
			newForClient++
			if subscriptionRefCount[symbol] == 0 {
				newGlobal++
			}
		}
		if len(existing)+newForClient > maxSubscriptionsPerClient ||
			len(subscriptionRefCount)+newGlobal > maxSubscriptionSymbols {
			return nil, false
		}
	}
	if len(symbols) == 0 {
		return nil, true
	}
	if existing == nil {
		existing = make(map[string]struct{})
		subscriptionsByClient[conn] = existing
	}
	for _, symbol := range symbols {
		if _, subscribed := existing[symbol]; subscribed {
			continue
		}
		existing[symbol] = struct{}{}
		subscriptionRefCount[symbol]++
		clients, _ := clientSubs.LoadOrStore(symbol, &sync.Map{})
		clients.(*sync.Map).Store(conn, true)
	}
	return symbols, true
}

func removeSubscriptions(conn any) {
	removeSubscriptionSymbols(conn, nil)
}

// A nil symbol list removes every membership on disconnect.
func removeSubscriptionSymbols(conn any, symbols []string) {
	subscriptionsMu.Lock()
	defer subscriptionsMu.Unlock()

	for symbol := range subscriptionsByClient[conn] {
		if symbols != nil && !slices.Contains(symbols, symbol) {
			continue
		}
		delete(subscriptionsByClient[conn], symbol)
		subscriptionRefCount[symbol]--
		clients, _ := clientSubs.Load(symbol)
		clients.(*sync.Map).Delete(conn)
		if subscriptionRefCount[symbol] == 0 {
			delete(subscriptionRefCount, symbol)
			clientSubs.Delete(symbol)
			mdAwaiting.Delete(symbol)
		}
	}
	if len(subscriptionsByClient[conn]) == 0 {
		delete(subscriptionsByClient, conn)
	}
}

func marketDataReject(id string, kind protobuf.ErrorType, reason string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ErrorMessage{ErrorMessage: &protobuf.ErrorMessage{
		RequestId: id, ErrorType: kind, ErrorMessage: reason,
	}}}
}

func routeMarketDataValidation(ack *protobuf.MarketDataRequestAck) {
	r := pendingRequests.get(ack.RequestId, "", true, "market data")
	if r == nil {
		return
	}
	if ack.RejectReason != "" {
		r.marketDataResult <- false
		_ = writeFXPMessage(r.conn, marketDataReject(r.original, protobuf.ErrorType_SYMBOL_NOT_SUPPORTED, ack.RejectReason))
		return
	}
	r.marketDataResult <- true
}

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
