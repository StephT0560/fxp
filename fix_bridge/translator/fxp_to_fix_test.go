package translator

import (
	"bytes"
	"strconv"
	"testing"

	"fix_bridge/codec"
	"go_gateway/protobuf"
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
			if got := TranslateFXPtoFIX(msg, "FXPBRIDGE", "CLIENT", 2); got != nil {
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
