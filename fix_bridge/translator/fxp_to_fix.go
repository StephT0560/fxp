// SPDX-License-Identifier: MIT

// fxp_to_fix.go — FXP Protobuf → FIX 4.4 translator
//
// Translates inbound FXP messages from the gateway into FIX frames
// for delivery to the connected FIX client.
//
// Supported translations:
//   ExecutionReport  → FIX ExecutionReport (tag 35=8)
//                      ExecType mapping:
//                        EXEC_NEW      → ExecType=0 (New)
//                        EXEC_FILLED   → ExecType=2 (Fill) or 1 (PartialFill)
//                        EXEC_CANCELED → ExecType=4 (Canceled)
//                        EXEC_REPLACED → ExecType=5 (Replaced)
//                        EXEC_REJECTED → ExecType=8 (Rejected)
//                        EXEC_EXPIRED  → ExecType=C (Expired)
//   MarketDataSnapshot     → FIX MarketDataSnapshotFullRefresh (tag 35=W)
//   MarketDataIncremental  → FIX MarketDataIncrementalRefresh  (tag 35=X)
//
// Prices are converted from FXP fixed-point integers (cents) back to
// FIX decimal strings ("18542" → "185.42").
//
// Reference: FIX Protocol 4.4 specification
// https://www.fixtrading.org/standards/fix-4-4/

package translator

import (
	"fmt"
	"log"
	"strconv"

	"fix_bridge/codec"
	"go_gateway/protobuf"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TranslateFXPtoFIX converts an inbound FXP message to a FIX frame.
// senderCompID and targetCompID are the bridge and FIX client identities.
// seqNum is the next outbound FIX sequence number (managed by the session).
// Returns nil if the message type does not produce a FIX output
// (e.g. internal gateway messages).
//
// mdReqID returns the MDReqID(262) of the client's active subscription for a
// symbol. Market data without a subscription (or with a nil lookup) is dropped.
func TranslateFXPtoFIX(
	msg *protobuf.FXPMessage,
	senderCompID, targetCompID string,
	seqNum int,
	mdReqID MDReqIDLookup,
) []byte {
	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_ExecutionReport:
		return translateExecutionReport(payload.ExecutionReport, senderCompID, targetCompID, seqNum)

	case *protobuf.FXPMessage_MarketDataSnapshot:
		reqID := lookupMDReqID(mdReqID, payload.MarketDataSnapshot.Symbol)
		if reqID == "" {
			return nil
		}
		return translateMarketDataSnapshot(payload.MarketDataSnapshot, senderCompID, targetCompID, seqNum, reqID)

	case *protobuf.FXPMessage_MarketDataIncremental:
		reqID := lookupMDReqID(mdReqID, payload.MarketDataIncremental.Symbol)
		if reqID == "" {
			return nil
		}
		return translateMarketDataIncremental(payload.MarketDataIncremental, senderCompID, targetCompID, seqNum, reqID)

	case *protobuf.FXPMessage_LogonAck:
		// Already handled during connect — not forwarded to FIX client
		return nil

	default:
		log.Printf("[translator] unhandled FXP payload type: %T", msg.Payload)
		return nil
	}
}

// MDReqIDLookup returns the MDReqID(262) subscribed for symbol, or "" if none.
type MDReqIDLookup func(symbol string) string

func lookupMDReqID(lookup MDReqIDLookup, symbol string) string {
	if lookup == nil {
		return ""
	}
	return lookup(symbol)
}

// ── ExecutionReport ───────────────────────────────────────────────────────────

func translateExecutionReport(
	rep *protobuf.ExecutionReport,
	senderCompID, targetCompID string,
	seqNum int,
) []byte {
	execType, err := mapExecType(protobuf.ExecutionType(rep.ExecutionType))
	if err != nil {
		log.Printf("[translator] dropping ExecutionReport %q: %v", rep.OrderId, err)
		return nil
	}
	ordStatus, err := mapOrdStatus(protobuf.OrderStatus(rep.OrderStatus))
	if err != nil {
		log.Printf("[translator] dropping ExecutionReport %q: %v", rep.OrderId, err)
		return nil
	}
	side, err := mapSideToFIX(rep.Side)
	if err != nil {
		log.Printf("[translator] dropping ExecutionReport %q: %v", rep.OrderId, err)
		return nil
	}

	b := codec.NewBuilder(codec.MsgTypeExecutionReport, senderCompID, targetCompID, seqNum)

	// Required fields
	b.Set(codec.TagOrderID, rep.OrderId)
	b.Set(codec.TagExecID, rep.ExecutionId)
	b.Set(codec.TagExecType, execType)
	b.Set(codec.TagOrdStatus, ordStatus)
	b.Set(codec.TagSymbol, rep.Symbol)
	b.Set(codec.TagSide, side)
	if rep.OrderQuantity > 0 {
		b.SetInt(codec.TagOrderQty, int(rep.OrderQuantity))
	}

	// ClOrdID — use client_order_id if set, fall back to order_id
	clOrdID := rep.ClientOrderId
	if clOrdID == "" {
		clOrdID = rep.OrderId
	}
	b.Set(codec.TagClOrdID, clOrdID)

	// OrigClOrdID — set on replace reports
	if rep.OrigOrderId != "" {
		b.Set(codec.TagOrigClOrdID, rep.OrigOrderId)
	}

	// Account
	if rep.Account != "" {
		b.Set(codec.TagAccount, rep.Account)
	}

	// Quantity fields
	b.SetInt(codec.TagLeavesQty, int(rep.LeavesQuantity))
	b.SetInt(codec.TagCumQty, int(rep.CumQuantity))

	// Fill-specific fields
	if rep.ExecutedQuantity > 0 {
		b.SetInt(codec.TagLastQty, int(rep.ExecutedQuantity))
		b.Set(codec.TagLastPx, formatFixedPoint(rep.ExecutionPrice))
	}

	// Average price (required on every ExecutionReport)
	b.Set(codec.TagAvgPx, formatFixedPoint(rep.AvgPx))

	// Reject reason
	if rep.RejectReason != "" {
		b.Set(codec.TagText, rep.RejectReason)
		b.Set(codec.TagOrdRejReason, codec.OrdRejReasonOther)
	}

	// TransactTime
	if rep.Timestamp != nil {
		b.Set(codec.TagTransactTime, formatProtoTime(rep.Timestamp))
	}

	return b.Build()
}

// ── MarketDataSnapshotFullRefresh (W) ─────────────────────────────────────────

func translateMarketDataSnapshot(
	snap *protobuf.MarketDataSnapshot,
	senderCompID, targetCompID string,
	seqNum int,
	mdReqID string,
) []byte {
	b := codec.NewBuilder(codec.MsgTypeMarketDataSnapshot, senderCompID, targetCompID, seqNum)
	b.Set(codec.TagMDReqID, mdReqID)
	b.Set(codec.TagSymbol, snap.Symbol)

	// Total entry count = bids + asks
	numEntries := len(snap.BidLevels) + len(snap.AskLevels)
	b.SetInt(codec.TagNoMDEntries, numEntries)

	// Repeating group entries use Append so every level is emitted.
	for _, bid := range snap.BidLevels {
		b.Append(codec.TagMDEntryType, "0") // Bid
		b.Append(codec.TagMDEntryPx, formatFixedPoint(bid.Price))
		b.Append(codec.TagMDEntrySize, strconv.Itoa(int(bid.Size)))
	}
	for _, ask := range snap.AskLevels {
		b.Append(codec.TagMDEntryType, "1") // Offer
		b.Append(codec.TagMDEntryPx, formatFixedPoint(ask.Price))
		b.Append(codec.TagMDEntrySize, strconv.Itoa(int(ask.Size)))
	}

	if snap.Timestamp != nil {
		b.Set(codec.TagTransactTime, formatProtoTime(snap.Timestamp))
	}

	return b.Build()
}

// ── MarketDataIncrementalRefresh (X) ─────────────────────────────────────────

func translateMarketDataIncremental(
	inc *protobuf.MarketDataIncrementalUpdate,
	senderCompID, targetCompID string,
	seqNum int,
	mdReqID string,
) []byte {
	b := codec.NewBuilder(codec.MsgTypeMarketDataIncremental, senderCompID, targetCompID, seqNum)
	b.Set(codec.TagMDReqID, mdReqID)

	type entry struct {
		change    *protobuf.MarketOrderUpdate
		entryType string
	}
	entries := make([]entry, 0, len(inc.Changes))
	for _, change := range inc.Changes {
		entryType, ok := mapMDEntryType(change.Side)
		if !ok {
			log.Printf("[translator] dropping %s MD entry with side %v", inc.Symbol, change.Side)
			continue
		}
		entries = append(entries, entry{change, entryType})
	}
	if len(entries) == 0 {
		return nil
	}
	b.SetInt(codec.TagNoMDEntries, len(entries))

	// Repeating group entries use Append so every change is emitted.
	for _, e := range entries {
		b.Append(codec.TagMDUpdateAction, mapMDUpdateAction(e.change.UpdateType))
		b.Append(codec.TagMDEntryType, e.entryType)
		b.Append(codec.TagSymbol, inc.Symbol)
		b.Append(codec.TagMDEntryPx, formatFixedPoint(e.change.Price))
		b.Append(codec.TagMDEntrySize, strconv.Itoa(int(e.change.Quantity)))
	}

	if inc.Timestamp != nil {
		b.Set(codec.TagTransactTime, formatProtoTime(inc.Timestamp))
	}

	return b.Build()
}

// mapMDEntryType maps the book side of a level to MDEntryType(269).
func mapMDEntryType(side protobuf.OrderSide) (string, bool) {
	switch side {
	case protobuf.OrderSide_BUY:
		return "0", true // Bid
	case protobuf.OrderSide_SELL:
		return "1", true // Offer
	default:
		return "", false
	}
}

// ── Mapping helpers ───────────────────────────────────────────────────────────

func mapExecType(et protobuf.ExecutionType) (string, error) {
	switch et {
	case protobuf.ExecutionType_EXEC_NEW:
		return codec.ExecTypeNew, nil
	case protobuf.ExecutionType_EXEC_FILLED:
		return codec.ExecTypeTrade, nil
	case protobuf.ExecutionType_EXEC_CANCELED:
		return codec.ExecTypeCanceled, nil
	case protobuf.ExecutionType_EXEC_REPLACED:
		return codec.ExecTypeReplaced, nil
	case protobuf.ExecutionType_EXEC_REJECTED:
		return codec.ExecTypeRejected, nil
	case protobuf.ExecutionType_EXEC_EXPIRED:
		return codec.ExecTypeExpired, nil
	case protobuf.ExecutionType_EXEC_PENDING_CANCEL:
		return "6", nil
	case protobuf.ExecutionType_EXEC_PENDING_REPLACE:
		return "E", nil
	default:
		return "", fmt.Errorf("unknown or unspecified execution_type %d", et)
	}
}

func mapSideToFIX(side protobuf.OrderSide) (string, error) {
	switch side {
	case protobuf.OrderSide_BUY:
		return codec.SideBuy, nil
	case protobuf.OrderSide_SELL:
		return codec.SideSell, nil
	default:
		return "", fmt.Errorf("unknown or unspecified side %d", side)
	}
}

func mapOrdStatus(os protobuf.OrderStatus) (string, error) {
	switch os {
	case protobuf.OrderStatus_ORDER_NEW:
		return codec.OrdStatusNew, nil
	case protobuf.OrderStatus_ORDER_PENDING:
		return "A", nil
	case protobuf.OrderStatus_ORDER_PARTIALLY_FILLED:
		return codec.OrdStatusPartiallyFilled, nil
	case protobuf.OrderStatus_ORDER_FILLED:
		return codec.OrdStatusFilled, nil
	case protobuf.OrderStatus_ORDER_CANCELED:
		return codec.OrdStatusCanceled, nil
	case protobuf.OrderStatus_ORDER_REPLACED:
		return codec.OrdStatusReplaced, nil
	case protobuf.OrderStatus_ORDER_REJECTED:
		return codec.OrdStatusRejected, nil
	case protobuf.OrderStatus_ORDER_EXPIRED:
		return codec.OrdStatusExpired, nil
	case protobuf.OrderStatus_ORDER_PENDING_CANCEL:
		return "6", nil
	case protobuf.OrderStatus_ORDER_PENDING_REPLACE:
		return "E", nil
	default:
		return "", fmt.Errorf("unknown or unspecified order_status %d", os)
	}
}

func mapMDUpdateAction(updateType int32) string {
	switch updateType {
	case 0: // NewOrder
		return "0" // New
	case 1: // ModifyOrder
		return "1" // Change
	case 2: // CancelOrder
		return "2" // Delete
	default:
		return "0"
	}
}

// ── Price formatting ──────────────────────────────────────────────────────────

// formatFixedPoint converts FXP fixed-point cents to a FIX decimal price string.
// 18542 → "185.42", 18500 → "185.00", 50 → "0.50"
func formatFixedPoint(cents int64) string {
	if cents == 0 {
		return "0.00"
	}
	neg := ""
	if cents < 0 {
		neg = "-"
		cents = -cents
	}
	dollars := cents / 100
	c := cents % 100
	return fmt.Sprintf("%s%d.%02d", neg, dollars, c)
}

// formatProtoTime converts a timestamppb.Timestamp to FIX SendingTime format.
func formatProtoTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	t := ts.AsTime()
	return fmt.Sprintf("%04d%02d%02d-%02d:%02d:%02d.%03d",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(),
		t.Nanosecond()/1_000_000,
	)
}
