// fix_to_fxp.go — FIX 4.4 → FXP Protobuf translator
//
// Translates inbound FIX application messages into FXP proto messages
// for forwarding to the Go gateway.
//
// Supported message types:
//   D  NewOrderSingle           → protobuf.NewOrderSingle
//   F  OrderCancelRequest       → protobuf.OrderCancelRequest
//   G  OrderCancelReplaceRequest → protobuf.OrderCancelReplaceRequest
//   V  MarketDataRequest        → protobuf.MarketDataRequest
//
// Field mapping follows FIX 4.4 tag definitions. Prices are converted
// from FIX decimal strings (e.g. "185.42") to FXP fixed-point integers
// (e.g. 18542 cents). Quantities are integer in both formats.
//
// Validation errors are returned as Go errors — the caller (session handler)
// is responsible for sending a FIX Reject or BusinessMessageReject back to
// the client.
//
// Reference: FIX Protocol 4.4 specification
// https://www.fixtrading.org/standards/fix-4-4/

package translator

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"fix_bridge/codec"
	"go_gateway/protobuf"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TranslateFIXtoFXP converts a parsed FIX message to an FXP FXPMessage.
// Returns an error if required fields are missing or invalid.
func TranslateFIXtoFXP(msg *codec.Message) (*protobuf.FXPMessage, error) {
	switch msg.MsgType() {
	case codec.MsgTypeNewOrderSingle:
		return translateNewOrder(msg)
	case codec.MsgTypeOrderCancelRequest:
		return translateCancelRequest(msg)
	case codec.MsgTypeOrderCancelReplaceRequest:
		return translateCancelReplaceRequest(msg)
	case codec.MsgTypeMarketDataRequest:
		return translateMarketDataRequest(msg)
	default:
		return nil, fmt.Errorf("unsupported FIX message type: %q", msg.MsgType())
	}
}

// ── NewOrderSingle (D → NewOrderSingle) ──────────────────────────────────────

func translateNewOrder(msg *codec.Message) (*protobuf.FXPMessage, error) {
	// Required fields
	clOrdID := msg.Get(codec.TagClOrdID)
	if clOrdID == "" {
		return nil, fmt.Errorf("NewOrderSingle missing ClOrdID (tag 11)")
	}
	symbol := msg.Get(codec.TagSymbol)
	if symbol == "" {
		return nil, fmt.Errorf("NewOrderSingle missing Symbol (tag 55)")
	}
	sideStr := msg.Get(codec.TagSide)
	if sideStr == "" {
		return nil, fmt.Errorf("NewOrderSingle missing Side (tag 54)")
	}
	ordTypeStr := msg.Get(codec.TagOrdType)
	if ordTypeStr == "" {
		return nil, fmt.Errorf("NewOrderSingle missing OrdType (tag 40)")
	}
	qtyStr := msg.Get(codec.TagOrderQty)
	if qtyStr == "" {
		return nil, fmt.Errorf("NewOrderSingle missing OrderQty (tag 38)")
	}

	// Parse quantity
	qty, err := strconv.ParseInt(qtyStr, 10, 32)
	if err != nil || qty <= 0 {
		return nil, fmt.Errorf("invalid OrderQty %q", qtyStr)
	}

	// Map Side
	side, err := mapSide(sideStr)
	if err != nil {
		return nil, err
	}

	// Map OrdType
	ordType, err := mapOrdType(ordTypeStr)
	if err != nil {
		return nil, err
	}

	// Map TimeInForce (optional — default to Day)
	tifStr := msg.Get(codec.TagTimeInForce)
	if tifStr == "" {
		tifStr = codec.TIFDay
	}
	tif, err := mapTimeInForce(tifStr)
	if err != nil {
		return nil, err
	}

	// Parse limit price (required for Limit and StopLimit)
	var price int64
	priceStr := msg.Get(codec.TagPrice)
	if priceStr != "" {
		price, err = parseFixedPoint(priceStr)
		if err != nil {
			return nil, fmt.Errorf("invalid Price %q: %w", priceStr, err)
		}
	} else if ordType == protobuf.OrderType_LIMIT || ordType == protobuf.OrderType_STOP_LIMIT {
		return nil, fmt.Errorf("Price (tag 44) required for OrdType %q", ordTypeStr)
	}

	// Parse stop price (required for Stop and StopLimit)
	var stopPx int64
	stopPxStr := msg.Get(codec.TagStopPx)
	if stopPxStr != "" {
		stopPx, err = parseFixedPoint(stopPxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid StopPx %q: %w", stopPxStr, err)
		}
	} else if ordType == protobuf.OrderType_STOP || ordType == protobuf.OrderType_STOP_LIMIT {
		return nil, fmt.Errorf("StopPx (tag 99) required for OrdType %q", ordTypeStr)
	}

	// Optional fields
	account    := msg.Get(codec.TagAccount)
	expireDate := msg.Get(codec.TagExpireDate)
	sender     := msg.Get(codec.TagSenderCompID)

	order := &protobuf.NewOrderSingle{
		Header: &protobuf.FXPHeader{
			ProtocolVersion: "1.1",
		},
		OrderId:        clOrdID,
		ClientOrderId:  clOrdID, // FIX ClOrdID maps to both
		Sender:         sender,
		Receiver:       "FXPExchange",
		Symbol:         symbol,
		Side:           side,
		Quantity:       int32(qty),
		OrderType:      ordType,
		TimeInForce:    tif,
		Price:          price,
		StopPrice:      stopPx,
		Account:        account,
		ExpireDate:     expireDate,
		Timestamp:      timestamppb.Now(),
	}

	return &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_NewOrder{NewOrder: order},
	}, nil
}

// ── OrderCancelRequest (F → OrderCancelRequest) ───────────────────────────────

func translateCancelRequest(msg *codec.Message) (*protobuf.FXPMessage, error) {
	clOrdID := msg.Get(codec.TagClOrdID)
	if clOrdID == "" {
		return nil, fmt.Errorf("OrderCancelRequest missing ClOrdID (tag 11)")
	}
	origClOrdID := msg.Get(codec.TagOrigClOrdID)
	if origClOrdID == "" {
		return nil, fmt.Errorf("OrderCancelRequest missing OrigClOrdID (tag 41)")
	}
	symbol := msg.Get(codec.TagSymbol)
	if symbol == "" {
		return nil, fmt.Errorf("OrderCancelRequest missing Symbol (tag 55)")
	}

	cancel := &protobuf.OrderCancelRequest{
		Header: &protobuf.FXPHeader{
			ProtocolVersion: "1.1",
		},
		OrderId:        origClOrdID, // the order being cancelled
		ClientCancelId: clOrdID,     // this request's ID
		Symbol:         symbol,
		Sender:         msg.Get(codec.TagSenderCompID),
		Receiver:       "FXPExchange",
		Timestamp:      timestamppb.Now(),
	}

	return &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_OrderCancel{OrderCancel: cancel},
	}, nil
}

// ── OrderCancelReplaceRequest (G → OrderCancelReplaceRequest) ─────────────────

func translateCancelReplaceRequest(msg *codec.Message) (*protobuf.FXPMessage, error) {
	clOrdID := msg.Get(codec.TagClOrdID)
	if clOrdID == "" {
		return nil, fmt.Errorf("OrderCancelReplaceRequest missing ClOrdID (tag 11)")
	}
	origClOrdID := msg.Get(codec.TagOrigClOrdID)
	if origClOrdID == "" {
		return nil, fmt.Errorf("OrderCancelReplaceRequest missing OrigClOrdID (tag 41)")
	}
	symbol := msg.Get(codec.TagSymbol)
	if symbol == "" {
		return nil, fmt.Errorf("OrderCancelReplaceRequest missing Symbol (tag 55)")
	}

	// New price (optional — 0 means no change)
	var newPrice int64
	if priceStr := msg.Get(codec.TagPrice); priceStr != "" {
		var err error
		newPrice, err = parseFixedPoint(priceStr)
		if err != nil {
			return nil, fmt.Errorf("invalid Price %q: %w", priceStr, err)
		}
	}

	// New quantity (optional — 0 means no change)
	var newQty int32
	if qtyStr := msg.Get(codec.TagOrderQty); qtyStr != "" {
		qty, err := strconv.ParseInt(qtyStr, 10, 32)
		if err != nil || qty <= 0 {
			return nil, fmt.Errorf("invalid OrderQty %q", qtyStr)
		}
		newQty = int32(qty)
	}

	cr := &protobuf.OrderCancelReplaceRequest{
		Header: &protobuf.FXPHeader{
			ProtocolVersion: "1.1",
		},
		OrderId:         origClOrdID, // order being replaced
		NewOrderId:      clOrdID,     // replacement order ID
		ClientReplaceId: clOrdID,
		Symbol:          symbol,
		Sender:          msg.Get(codec.TagSenderCompID),
		Receiver:        "FXPExchange",
		NewPrice:        newPrice,
		NewQuantity:     newQty,
		Timestamp:       timestamppb.Now(),
	}

	return &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_OrderCancelReplace{OrderCancelReplace: cr},
	}, nil
}

// ── MarketDataRequest (V → MarketDataRequest) ─────────────────────────────────

func translateMarketDataRequest(msg *codec.Message) (*protobuf.FXPMessage, error) {
	mdReqID := msg.Get(codec.TagMDReqID)
	if mdReqID == "" {
		return nil, fmt.Errorf("MarketDataRequest missing MDReqID (tag 262)")
	}

	// Parse repeating group: NoRelatedSym (tag 146) symbols
	// FIX repeating groups: tag 146 = count, followed by tag 55 entries
	numSymStr := msg.Get(codec.TagNoRelatedSym)
	numSym, _ := strconv.Atoi(numSymStr)

	// Collect all Symbol (tag 55) values from the message
	var symbols []string
	for _, f := range msg.Tags {
		if f.Tag == codec.TagSymbol && f.Value != "" {
			symbols = append(symbols, f.Value)
		}
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("MarketDataRequest missing Symbol entries (tag 55)")
	}
	// Truncate to declared count if present and consistent
	if numSym > 0 && numSym < len(symbols) {
		symbols = symbols[:numSym]
	}

	return &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{
			MarketDataRequest: &protobuf.MarketDataRequest{
				Symbols: symbols,
			},
		},
	}, nil
}

// ── Field mapping helpers ─────────────────────────────────────────────────────

func mapSide(s string) (protobuf.OrderSide, error) {
	switch s {
	case codec.SideBuy:
		return protobuf.OrderSide_BUY, nil
	case codec.SideSell, codec.SideSellShort:
		return protobuf.OrderSide_SELL, nil
	default:
		return 0, fmt.Errorf("unsupported Side value %q (tag 54)", s)
	}
}

func mapOrdType(s string) (protobuf.OrderType, error) {
	switch s {
	case codec.OrdTypeMarket:
		return protobuf.OrderType_MARKET, nil
	case codec.OrdTypeLimit:
		return protobuf.OrderType_LIMIT, nil
	case codec.OrdTypeStop:
		return protobuf.OrderType_STOP, nil
	case codec.OrdTypeStopLimit:
		return protobuf.OrderType_STOP_LIMIT, nil
	default:
		return 0, fmt.Errorf("unsupported OrdType value %q (tag 40)", s)
	}
}

func mapTimeInForce(s string) (protobuf.TimeInForce, error) {
	switch s {
	case codec.TIFDay:
		return protobuf.TimeInForce_DAY, nil
	case codec.TIFGTC:
		return protobuf.TimeInForce_GTC, nil
	case codec.TIFIOC:
		return protobuf.TimeInForce_IOC, nil
	case codec.TIFFOK:
		return protobuf.TimeInForce_FOK, nil
	case codec.TIFGTD:
		return protobuf.TimeInForce_GTD, nil
	case codec.TIFAtClose:
		return protobuf.TimeInForce_AT_CLOSE, nil
	case codec.TIFAtOpen:
		return protobuf.TimeInForce_AT_OPEN, nil
	default:
		return 0, fmt.Errorf("unsupported TimeInForce value %q (tag 59)", s)
	}
}

// parseFixedPoint converts a FIX decimal price string to fixed-point cents.
// "185.42" → 18542, "185" → 18500, "0.50" → 50
// Supports up to 2 decimal places (cent precision).
func parseFixedPoint(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty price string")
	}

	parts := strings.SplitN(s, ".", 2)
	dollars, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer part %q", parts[0])
	}

	var cents int64
	if len(parts) == 2 {
		// Pad or truncate to 2 decimal places
		dec := parts[1]
		if len(dec) > 2 {
			// Round at 2dp
			dec = dec[:2]
		} else if len(dec) == 1 {
			dec = dec + "0"
		}
		c, err := strconv.ParseInt(dec, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid decimal part %q", parts[1])
		}
		cents = c
	}

	if dollars < 0 {
		cents = -cents
	}

	result := dollars*100 + cents
	if result > math.MaxInt32 || result < math.MinInt32 {
		return 0, fmt.Errorf("price %q out of range", s)
	}
	return result, nil
}