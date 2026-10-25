// SPDX-License-Identifier: MIT
package translator

import (
	"fix_bridge/codec"
	"go_gateway/protobuf"
	"testing"
)

func TestMaxFloorTranslation(t *testing.T) {
	o, err := newOrderWith(t, map[int]string{111: "3"})
	if err != nil || o.DisplayQuantity != 3 {
		t.Fatalf("floor: %v %v", o, err)
	}
	for _, v := range []string{"-1", "2147483648", "1.5"} {
		if _, err := newOrderWith(t, map[int]string{111: v}); err == nil {
			t.Fatalf("invalid floor accepted %s", v)
		}
	}
	for _, v := range []string{"", "0", "2"} {
		fields := map[int]string{codec.TagClOrdID: "R", codec.TagOrigClOrdID: "I", codec.TagSymbol: "AAPL"}
		if v != "" {
			fields[111] = v
		}
		m, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderCancelReplaceRequest, fields))
		if err != nil {
			t.Fatal(err)
		}
		r := m.GetOrderCancelReplace()
		if (r.NewDisplayQuantity == nil) != (v == "") {
			t.Fatalf("presence %q: %v", v, r)
		}
		if v == "2" && r.GetNewDisplayQuantity() != 2 {
			t.Fatal(r)
		}
	}
	rep := &protobuf.ExecutionReport{OrderId: "I", Symbol: "AAPL", Side: protobuf.OrderSide_SELL, ExecutionType: protobuf.ExecutionType_EXEC_NEW, OrderStatus: protobuf.OrderStatus_ORDER_NEW, OrderQuantity: 10, LeavesQuantity: 10, DisplayQuantity: 3}
	m := parseER(t, translateExecutionReport(rep, "FXP", "CLIENT", 1))
	if m.Get(111) != "3" {
		t.Fatalf("MaxFloor not echoed: %q", m.Get(111))
	}
}
