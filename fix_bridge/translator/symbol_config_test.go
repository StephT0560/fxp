// SPDX-License-Identifier: MIT
package translator

import (
	"bytes"
	"fix_bridge/codec"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"testing"
)

func TestSymbolPriceTranslationAndMarketData(t *testing.T) {
	old := symbolconfig.Current()
	defer symbolconfig.Set(old)
	config, err := symbolconfig.Parse("P0,0,1,1,1,100\nP2,2,1,1,1,100\nP4,4,1,1,1,100\nP8,8,1,1,1,100")
	if err != nil {
		t.Fatal(err)
	}
	symbolconfig.Set(config)
	for _, tc := range []struct {
		symbol, price string
		units         int64
	}{{"P0", "12", 12}, {"P2", "12.34", 1234}, {"P4", "12.3456", 123456}, {"P8", "12.34567890", 1234567890}} {
		m := buildInbound(t, "D", map[int]string{11: "O", 55: tc.symbol, 54: "1", 38: "10", 40: "2", 44: tc.price})
		wire, e := TranslateFIXtoFXP(m)
		if e != nil {
			t.Fatal(e)
		}
		if wire.GetNewOrder().Price != tc.units {
			t.Fatal(tc)
		}
		raw := translateMarketDataSnapshot(&protobuf.MarketDataSnapshot{Symbol: tc.symbol, BidLevels: []*protobuf.MarketOrderBookEntry{{Price: tc.units, Size: 10}}}, "B", "T", 1, "REQ")
		parsed, e := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
		if e != nil || parsed.Get(270) != tc.price {
			t.Fatalf("%s: %v %v", tc.symbol, parsed, e)
		}
	}
}
