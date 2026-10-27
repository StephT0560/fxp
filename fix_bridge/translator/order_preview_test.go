// SPDX-License-Identifier: MIT
package translator

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fix_bridge/codec"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestRenderNewOrderGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/order_preview.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name  string
		Order json.RawMessage
		FIX   string
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			o := new(protobuf.NewOrderSingle)
			if e := protojson.Unmarshal(f.Order, o); e != nil {
				t.Fatal(e)
			}
			frame, e := RenderNewOrderFIX(o, nil)
			if e != nil {
				t.Fatal(e)
			}
			if got := strings.ReplaceAll(string(frame), "\x01", "|"); got != f.FIX {
				t.Fatalf("got %s\nwant %s", got, f.FIX)
			}
			m, e := codec.NewParser(bytes.NewReader(frame)).ReadMessage()
			if e != nil {
				t.Fatal(e)
			}
			round, e := TranslateFIXtoFXP(m)
			if e != nil {
				t.Fatal(e)
			}
			r := round.GetNewOrder()
			if r.ClientOrderId != o.ClientOrderId || r.Symbol != o.Symbol || r.Side != o.Side || r.Quantity != o.Quantity || r.Price != o.Price || r.StopPrice != o.StopPrice || r.OrderType != o.OrderType || r.TimeInForce != o.TimeInForce || r.StpMode != o.StpMode || r.StpGroup != o.StpGroup || r.DisplayQuantity != o.DisplayQuantity || r.ExpireDate != o.ExpireDate {
				t.Fatalf("roundtrip mismatch: %v", r)
			}
		})
	}
}

func TestRenderNewOrderRejectsLossyAndInjectedOrders(t *testing.T) {
	data, e := os.ReadFile("testdata/order_preview.json")
	if e != nil {
		t.Fatal(e)
	}
	var f []struct{ Order json.RawMessage }
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*protobuf.NewOrderSingle){
		func(o *protobuf.NewOrderSingle) { o.StpMode = protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_NONE },
		func(o *protobuf.NewOrderSingle) { o.ClientOrderId = "injected\x0135=A" },
		func(o *protobuf.NewOrderSingle) { o.StpGroup = "bad|field" },
		func(o *protobuf.NewOrderSingle) { o.OrderType = 999 },
		func(o *protobuf.NewOrderSingle) { o.Quantity = 0 },
		func(o *protobuf.NewOrderSingle) { o.Timestamp = nil },
		func(o *protobuf.NewOrderSingle) { o.TimeInForce = protobuf.TimeInForce_GTD },
	} {
		o := new(protobuf.NewOrderSingle)
		if e = protojson.Unmarshal(f[0].Order, o); e != nil {
			t.Fatal(e)
		}
		mutate(o)
		if _, e = RenderNewOrderFIX(o, nil); e == nil {
			t.Fatal("accepted unrepresentable order")
		}
	}
}

func TestRenderNewOrderUsesVenuePriceScale(t *testing.T) {
	data, e := os.ReadFile("testdata/order_preview.json")
	if e != nil {
		t.Fatal(e)
	}
	var f []struct{ Order json.RawMessage }
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	o := new(protobuf.NewOrderSingle)
	if e = protojson.Unmarshal(f[0].Order, o); e != nil {
		t.Fatal(e)
	}
	o.Price = 9007199254740993
	raw, e := RenderNewOrderFIX(o, symbolconfig.Config{"DEMO": {PriceScale: 8}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), "44=90071992.54740993\x01") {
		t.Fatalf("lost price precision: %s", raw)
	}
}
