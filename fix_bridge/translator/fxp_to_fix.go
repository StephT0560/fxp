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

	"fix_bridge/codec"
	"go_gateway/protobuf"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TranslateFXPtoFIX converts an inbound FXP message to a FIX frame.
// senderCompID and targetCompID are the bridge and FIX client identities.
// seqNum is the next outbound FIX sequence number (managed by the session).
// Returns nil if the message type does not produce a FIX output
// (e.g. internal gateway messages).
func TranslateFXPtoFIX(
	msg *protobuf.FXPMessage,
	senderCompID, targetCompID string,
	seqNum int,
) []byte {
	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_ExecutionReport:
		return translateExecutionReport(payload.ExecutionReport, senderCompID, targetCompID, seqNum)

	case *protobuf.FXPMessage_MarketDataSnapshot:
		return translateMarketDataSnapshot(payload.MarketDataSnapshot, senderCompID, targetCompID, seqNum)

	case *protobuf.FXPMessage_MarketDataIncremental:
		return translateMarketDataIncremental(payload.MarketDataIncremental, senderCompID, targetCompID, seqNum)

	case *protobuf.FXPMessage_LogonAck:
		// Already handled during connect — not forwarded to FIX client
		return nil

	default:
		log.Printf("[translator] unhandled FXP payload type: %T", msg.Payload)
		return nil
	}
}

// ── ExecutionReport ───────────────────────────────────────────────────────────

func translateExecutionReport(
	rep *protobuf.ExecutionReport,
	senderCompID, targetCompID string,
	seqNum int,
) []byte {
	execType  := mapExecType(protobuf.ExecutionType(rep.ExecutionType))
	ordStatus := mapOrdStatus(protobuf.OrderStatus(rep.OrderStatus))

	b := codec.NewBuilder(codec.MsgTypeExecutionReport, senderCompID, targetCompID, seqNum)

	// Required fields
	b.Set(codec.TagOrderID,  rep.OrderId)
	b.Set(codec.TagExecID,   rep.ExecutionId)
	b.Set(codec.TagExecType, execType)
	b.Set(codec.TagOrdStatus, ordStatus)
	b.Set(codec.TagSymbol,   rep.Symbol)

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
	b.SetInt(codec.TagCumQty,    int(rep.CumQuantity))

	// Fill-specific fields
	if rep.ExecutedQuantity > 0 {
		b.SetInt(codec.TagLastQty, int(rep.ExecutedQuantity))
		b.Set(codec.TagLastPx,    formatFixedPoint(rep.ExecutionPrice))
	}

	// Average price
	if rep.AvgPx > 0 {
		b.Set(codec.TagAvgPx, formatFixedPoint(rep.AvgPx))
	}

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
) []byte {
	b := codec.NewBuilder(codec.MsgTypeMarketDataSnapshot, senderCompID, targetCompID, seqNum)
	b.Set(codec.TagSymbol, snap.Symbol)

	// Total entry count = bids + asks
	numEntries := len(snap.BidLevels) + len(snap.AskLevels)
	b.SetInt(codec.TagNoMDEntries, numEntries)

	// Bid levels (MDEntryType=0)
	for _, bid := range snap.BidLevels {
		b.Set(codec.TagMDEntryType, "0") // Bid
		b.Set(codec.TagMDEntryPx,   formatFixedPoint(bid.Price))
		b.SetInt(codec.TagMDEntrySize, int(bid.Size))
	}

	// Ask levels (MDEntryType=1)
	for _, ask := range snap.AskLevels {
		b.Set(codec.TagMDEntryType, "1") // Offer
		b.Set(codec.TagMDEntryPx,   formatFixedPoint(ask.Price))
		b.SetInt(codec.TagMDEntrySize, int(ask.Size))
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
) []byte {
	b := codec.NewBuilder(codec.MsgTypeMarketDataIncremental, senderCompID, targetCompID, seqNum)

	b.SetInt(codec.TagNoMDEntries, len(inc.Changes))

	for _, change := range inc.Changes {
		// MDUpdateAction: 0=New, 1=Change, 2=Delete
		updateAction := mapMDUpdateAction(change.UpdateType)
		b.Set(codec.TagMDUpdateAction, updateAction)

		// MDEntryType — price level changes don't carry side in FXP incremental,
		// so we emit without MDEntryType (or caller can set it if available)
		b.Set(codec.TagSymbol,       inc.Symbol)
		b.Set(codec.TagMDEntryPx,    formatFixedPoint(change.Price))
		b.SetInt(codec.TagMDEntrySize, int(change.Quantity))
	}

	if inc.Timestamp != nil {
		b.Set(codec.TagTransactTime, formatProtoTime(inc.Timestamp))
	}

	return b.Build()
}

// ── Mapping helpers ───────────────────────────────────────────────────────────

func mapExecType(et protobuf.ExecutionType) string {
	switch et {
	case protobuf.ExecutionType_EXEC_NEW:
		return codec.ExecTypeNew
	case protobuf.ExecutionType_EXEC_FILLED:
		return codec.ExecTypeFill
	case protobuf.ExecutionType_EXEC_CANCELED:
		return codec.ExecTypeCanceled
	case protobuf.ExecutionType_EXEC_REPLACED:
		return codec.ExecTypeReplaced
	case protobuf.ExecutionType_EXEC_REJECTED:
		return codec.ExecTypeRejected
	case protobuf.ExecutionType_EXEC_EXPIRED:
		return codec.ExecTypeExpired
	default:
		return codec.ExecTypeNew // safe default — EXEC_NEW = 0 = proto3 default
	}
}

func mapOrdStatus(os protobuf.OrderStatus) string {
	switch os {
	case protobuf.OrderStatus_ORDER_NEW:
		return codec.OrdStatusNew
	case protobuf.OrderStatus_ORDER_PARTIALLY_FILLED:
		return codec.OrdStatusPartiallyFilled
	case protobuf.OrderStatus_ORDER_FILLED:
		return codec.OrdStatusFilled
	case protobuf.OrderStatus_ORDER_CANCELED:
		return codec.OrdStatusCanceled
	case protobuf.OrderStatus_ORDER_REPLACED:
		return codec.OrdStatusReplaced
	case protobuf.OrderStatus_ORDER_REJECTED:
		return codec.OrdStatusRejected
	case protobuf.OrderStatus_ORDER_EXPIRED:
		return codec.OrdStatusExpired
	default:
		return codec.OrdStatusNew
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
		return "0"
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