// SPDX-License-Identifier: MIT
package translator

import (
	"bytes"
	"fix_bridge/codec"
	"go_gateway/protobuf"
	"testing"
)

func TestOrderStatusTranslation(t *testing.T) {
	for _, typ := range []string{"H", "AF"} {
		fields := map[int]string{35: typ, 49: "T", 11: "O1", 55: "MSFT", 54: "1", 584: "M1", 585: "7", 790: "S1"}
		m, err := TranslateFIXtoFXP(&codec.Message{Fields: fields})
		if err != nil {
			t.Fatal(err)
		}
		q := m.GetOrderStatusRequest()
		if q == nil || q.Sender != "T" {
			t.Fatalf("bad query %+v", m)
		}
		if typ == "H" && q.OrderId != "O1" {
			t.Fatal(q)
		}
		if typ == "AF" && q.OrderId != "" {
			t.Fatal(q)
		}
	}
	raw := StatusExecutionReport(&protobuf.ExecutionReport{ExecutionType: protobuf.ExecutionType_EXEC_ORDER_STATUS, OrderStatus: protobuf.OrderStatus_ORDER_PARTIALLY_FILLED, Side: protobuf.OrderSide_BUY, OrderId: "O1", OrderQuantity: 10, CumQuantity: 4, LeavesQuantity: 6, AvgPx: 100, Symbol: "MSFT"}, "B", "T", 3, "S1", false, true, 1)
	m, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if m.Get(150) != "I" || m.Get(39) != "1" || m.Get(790) != "S1" || m.Get(151) != "6" {
		t.Fatalf("bad status %+v", m.Fields)
	}
}
