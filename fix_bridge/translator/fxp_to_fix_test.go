// SPDX-License-Identifier: MIT

package translator

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
	"time"

	"fix_bridge/codec"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func parseER(t *testing.T, raw []byte) *codec.Message {
	t.Helper()
	m, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func TestExecutionReportFieldMapping(t *testing.T) {
	cases := []struct {
		name                     string
		rep                      *protobuf.ExecutionReport
		execType, ordStatus      string
		lastQty, lastPx          string
		leaves, cum, avgPx, side string
	}{
		{"partial", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_FILLED, OrderStatus: protobuf.OrderStatus_ORDER_PARTIALLY_FILLED,
			ExecutionPrice: 10000, ExecutedQuantity: 4, LeavesQuantity: 6, CumQuantity: 4, AvgPx: 10000, Side: protobuf.OrderSide_SELL, OrderQuantity: 10},
			"F", "1", "4", "100.00", "6", "4", "100.00", "2"},
		{"full", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_FILLED, OrderStatus: protobuf.OrderStatus_ORDER_FILLED,
			ExecutionPrice: 10200, ExecutedQuantity: 6, LeavesQuantity: 0, CumQuantity: 10, AvgPx: 10120, Side: protobuf.OrderSide_BUY, OrderQuantity: 10},
			"F", "2", "6", "102.00", "0", "10", "101.20", "1"},
		{"new ack", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_NEW, OrderStatus: protobuf.OrderStatus_ORDER_NEW,
			LeavesQuantity: 10, Side: protobuf.OrderSide_BUY, OrderQuantity: 10},
			"0", "0", "", "", "10", "0", "0.00", "1"},
		{"ioc remainder", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_CANCELED, OrderStatus: protobuf.OrderStatus_ORDER_CANCELED,
			CumQuantity: 5, AvgPx: 10000, Side: protobuf.OrderSide_BUY, OrderQuantity: 8},
			"4", "4", "", "", "0", "5", "100.00", "1"},
		{"reject", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_REJECTED, OrderStatus: protobuf.OrderStatus_ORDER_REJECTED,
			RejectReason: "bad", Side: protobuf.OrderSide_SELL, OrderQuantity: 5},
			"8", "8", "", "", "0", "0", "0.00", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.rep.OrderId, tc.rep.ExecutionId, tc.rep.Symbol, tc.rep.ClientOrderId = "O1", "X1", "AAPL", "CL1"
			m := parseER(t, translateExecutionReport(tc.rep, "FXPBRIDGE", "CLIENT", 2))
			want := map[int]string{
				codec.TagExecType: tc.execType, codec.TagOrdStatus: tc.ordStatus,
				codec.TagLastQty: tc.lastQty, codec.TagLastPx: tc.lastPx,
				codec.TagLeavesQty: tc.leaves, codec.TagCumQty: tc.cum, codec.TagAvgPx: tc.avgPx,
				codec.TagSide: tc.side, codec.TagOrderQty: strconv.Itoa(int(tc.rep.OrderQuantity)),
				codec.TagOrderID: "O1", codec.TagExecID: "X1", codec.TagClOrdID: "CL1", codec.TagSymbol: "AAPL",
			}
			for tag, v := range want {
				if got := m.Get(tag); got != v {
					t.Errorf("tag %d = %q, want %q", tag, got, v)
				}
			}
		})
	}
}

func TestExecutionReportWithoutExecIDGetsUniqueSyntheticValue(t *testing.T) {
	rep := &protobuf.ExecutionReport{
		OrderId: "REJECT", Symbol: "AAPL", Side: protobuf.OrderSide_BUY,
		ExecutionType: protobuf.ExecutionType_EXEC_REJECTED,
		OrderStatus:   protobuf.OrderStatus_ORDER_REJECTED,
	}
	first := parseER(t, translateExecutionReport(rep, "FXPBRIDGE", "CLIENT", 2)).Get(codec.TagExecID)
	second := parseER(t, translateExecutionReport(rep, "FXPBRIDGE", "CLIENT", 3)).Get(codec.TagExecID)
	if first == "" || second == "" || first == second {
		t.Fatalf("synthetic ExecIDs = %q and %q, want distinct non-empty values", first, second)
	}

	rep.ExecutionId = "core-exec-id"
	preserved := parseER(t, translateExecutionReport(rep, "FXPBRIDGE", "CLIENT", 4)).Get(codec.TagExecID)
	if preserved != "core-exec-id" {
		t.Fatalf("non-empty ExecID = %q, want core-exec-id", preserved)
	}
}

func TestExpiredExecutionReportMapping(t *testing.T) {
	rep := &protobuf.ExecutionReport{
		OrderId: "GTD", ExecutionId: "X1", Symbol: "AAPL", Side: protobuf.OrderSide_BUY,
		ExecutionType: protobuf.ExecutionType_EXEC_EXPIRED, OrderStatus: protobuf.OrderStatus_ORDER_EXPIRED,
		OrderQuantity: 10, CumQuantity: 4, AvgPx: 100, LeavesQuantity: 0,
	}
	msg := parseER(t, translateExecutionReport(rep, "FXPBRIDGE", "CLIENT", 2))
	for tag, want := range map[int]string{150: "C", 39: "C", 151: "0", 14: "4", 6: "1.00"} {
		if got := msg.Get(tag); got != want {
			t.Errorf("tag %d=%q, want %q", tag, got, want)
		}
	}
}

func TestUnspecifiedExecutionReportEnumsAreDropped(t *testing.T) {
	tests := []struct {
		name string
		rep  *protobuf.ExecutionReport
	}{
		{"execution type", &protobuf.ExecutionReport{ExecutionType: 0, OrderStatus: protobuf.OrderStatus_ORDER_NEW, Side: protobuf.OrderSide_BUY}},
		{"order status", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_NEW, OrderStatus: 0, Side: protobuf.OrderSide_BUY}},
		{"side", &protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_NEW, OrderStatus: protobuf.OrderStatus_ORDER_NEW, Side: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: tc.rep}}
			if got := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 2, nil); got != nil {
				t.Fatalf("unspecified %s produced FIX output: %q", tc.name, got)
			}
		})
	}
}

func TestFormatFixedPoint(t *testing.T) {
	tests := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{50, "0.50"},
		{-50, "-0.50"},
		{-12345, "-123.45"},
		{18500, "185.00"},
	}
	for _, tc := range tests {
		if got := formatFixedPoint(tc.cents); got != tc.want {
			t.Errorf("formatFixedPoint(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func countTags(m *codec.Message) map[int]int {
	n := map[int]int{}
	for _, f := range m.Tags {
		n[f.Tag]++
	}
	return n
}

func mdReqIDs(ids map[string]string) MDReqIDLookup {
	return func(symbol string) string { return ids[symbol] }
}

func TestMarketDataSnapshotRepeatingGroup(t *testing.T) {
	snap := &protobuf.MarketDataSnapshot{
		Symbol: "AAPL",
		BidLevels: []*protobuf.MarketOrderBookEntry{
			{Price: 10000, Size: 5}, {Price: 9990, Size: 7},
		},
		AskLevels: []*protobuf.MarketOrderBookEntry{
			{Price: 10010, Size: 3}, {Price: 10020, Size: 9}, {Price: 10030, Size: 1},
		},
	}
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataSnapshot{MarketDataSnapshot: snap}}
	raw := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 3, mdReqIDs(map[string]string{"AAPL": "REQ1"}))
	m := parseER(t, raw)
	const n = 5
	if m.MsgType() != codec.MsgTypeMarketDataSnapshot || m.Get(codec.TagNoMDEntries) != strconv.Itoa(n) {
		t.Fatalf("35=%s 268=%s, want W and %d", m.MsgType(), m.Get(codec.TagNoMDEntries), n)
	}
	if m.Get(codec.TagMDReqID) != "REQ1" || m.Get(codec.TagSymbol) != "AAPL" {
		t.Fatalf("262=%q 55=%q, want REQ1 and AAPL", m.Get(codec.TagMDReqID), m.Get(codec.TagSymbol))
	}
	counts := countTags(m)
	for _, tag := range []int{codec.TagMDEntryType, codec.TagMDEntryPx, codec.TagMDEntrySize} {
		if counts[tag] != n {
			t.Errorf("tag %d occurs %d times, want %d", tag, counts[tag], n)
		}
	}
	var got []string
	for _, f := range m.Tags {
		if f.Tag == codec.TagMDEntryType || f.Tag == codec.TagMDEntryPx || f.Tag == codec.TagMDEntrySize {
			got = append(got, f.Value)
		}
	}
	want := []string{"0", "100.00", "5", "0", "99.90", "7", "1", "100.10", "3", "1", "100.20", "9", "1", "100.30", "1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("group entries = %v, want %v", got, want)
	}
}

func TestMarketDataIncrementalRepeatingGroup(t *testing.T) {
	inc := &protobuf.MarketDataIncrementalUpdate{
		Symbol: "AAPL",
		Changes: []*protobuf.MarketOrderUpdate{
			{UpdateType: 0, Price: 10000, Quantity: 5, Side: protobuf.OrderSide_BUY},
			{UpdateType: 1, Price: 10100, Quantity: 7, Side: protobuf.OrderSide_SELL},
			{UpdateType: 2, Price: 10200, Quantity: 9, Side: protobuf.OrderSide_SELL},
		},
	}
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataIncremental{MarketDataIncremental: inc}}
	raw := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 4, mdReqIDs(map[string]string{"AAPL": "REQ2"}))
	m := parseER(t, raw)
	const n = 3
	if m.MsgType() != codec.MsgTypeMarketDataIncremental || m.Get(codec.TagNoMDEntries) != strconv.Itoa(n) {
		t.Fatalf("35=%s 268=%s, want X and %d", m.MsgType(), m.Get(codec.TagNoMDEntries), n)
	}
	if m.Get(codec.TagMDReqID) != "REQ2" {
		t.Fatalf("262=%q, want REQ2", m.Get(codec.TagMDReqID))
	}
	counts := countTags(m)
	for _, tag := range []int{codec.TagMDUpdateAction, codec.TagMDEntryType, codec.TagSymbol, codec.TagMDEntryPx, codec.TagMDEntrySize} {
		if counts[tag] != n {
			t.Errorf("tag %d occurs %d times, want %d", tag, counts[tag], n)
		}
	}
	var got []string
	inGroup := false
	for _, f := range m.Tags {
		if f.Tag == codec.TagNoMDEntries {
			inGroup = true
			continue
		}
		if inGroup && f.Tag != codec.TagTransactTime && f.Tag != codec.TagCheckSum {
			got = append(got, fmt.Sprintf("%d=%s", f.Tag, f.Value))
		}
	}
	want := []string{
		"279=0", "269=0", "55=AAPL", "270=100.00", "271=5",
		"279=1", "269=1", "55=AAPL", "270=101.00", "271=7",
		"279=2", "269=1", "55=AAPL", "270=102.00", "271=9",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("group = %v, want %v", got, want)
	}
}

func TestMarketDataWithoutSubscriptionIsDropped(t *testing.T) {
	inc := &protobuf.MarketDataIncrementalUpdate{Symbol: "AAPL", Changes: []*protobuf.MarketOrderUpdate{
		{Price: 10000, Quantity: 5, Side: protobuf.OrderSide_BUY},
	}}
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataIncremental{MarketDataIncremental: inc}}
	if got := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 4, mdReqIDs(nil)); got != nil {
		t.Fatalf("unsolicited market data produced FIX output: %q", got)
	}
	if got := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 4, nil); got != nil {
		t.Fatalf("nil lookup produced FIX output: %q", got)
	}
}

func TestTradeCaptureReportProducesFIXIncrementalTradeEntry(t *testing.T) {
	old := symbolconfig.Current()
	defer symbolconfig.Set(old)
	config, err := symbolconfig.Parse("P3,3,1,1,1,100")
	if err != nil {
		t.Fatal(err)
	}
	symbolconfig.Set(config)
	report := &protobuf.TradeCaptureReport{
		Symbol: "P3", TradeId: "P3:7", TradePrice: 10105, TradeQuantity: 30,
		AggressorSide: protobuf.OrderSide_SELL, Seq: 7, IsAuction: true,
		Timestamp: timestamppb.New(time.Date(2024, 1, 2, 3, 4, 5, 678_000_000, time.UTC)),
	}
	raw := TranslateFXPtoFIX(
		&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_TradeCaptureReport{TradeCaptureReport: report}},
		"FXPBRIDGE", "CLIENT", 4, mdReqIDs(map[string]string{"P3": "REQ7"}),
	)
	msg := parseER(t, raw)
	if msg.MsgType() != "X" {
		t.Fatalf("35=%q, want X", msg.MsgType())
	}
	var got []string
	for _, field := range msg.Tags {
		switch field.Tag {
		case codec.TagMDReqID, codec.TagNoMDEntries, codec.TagMDUpdateAction,
			codec.TagMDEntryType, codec.TagSymbol, codec.TagMDEntryPx,
			codec.TagMDEntrySize, codec.TagMDEntryTime:
			got = append(got, strconv.Itoa(field.Tag)+"="+field.Value)
		}
	}
	want := []string{
		"262=REQ7", "268=1", "279=0", "269=2", "55=P3",
		"270=10.105", "271=30", "273=03:04:05.678",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("FIX tags = %v, want %v", got, want)
	}
	allowed := map[int]bool{
		8: true, 9: true, 10: true, 34: true, 35: true, 49: true, 52: true, 56: true,
		262: true, 268: true, 269: true, 270: true, 271: true, 273: true,
		279: true, 55: true,
	}
	for _, field := range msg.Tags {
		if field.Tag == codec.TagTransactTime {
			t.Fatal("trade 35=X unexpectedly contains TransactTime (60)")
		}
		if !allowed[field.Tag] {
			t.Fatalf("unexpected FIX field %d=%q", field.Tag, field.Value)
		}
	}
}

func TestMarketDataIncrementalSkipsUnspecifiedSide(t *testing.T) {
	inc := &protobuf.MarketDataIncrementalUpdate{Symbol: "AAPL", Changes: []*protobuf.MarketOrderUpdate{
		{Price: 10000, Quantity: 5, Side: protobuf.OrderSide_BUY},
		{Price: 10100, Quantity: 7},
	}}
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataIncremental{MarketDataIncremental: inc}}
	m := parseER(t, TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 4, mdReqIDs(map[string]string{"AAPL": "R"})))
	if m.Get(codec.TagNoMDEntries) != "1" || countTags(m)[codec.TagMDEntryPx] != 1 {
		t.Fatalf("268=%s with %d entries, want 1", m.Get(codec.TagNoMDEntries), countTags(m)[codec.TagMDEntryPx])
	}
}
