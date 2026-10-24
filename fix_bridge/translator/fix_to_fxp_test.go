package translator

import (
	"bytes"
	"testing"

	"fix_bridge/codec"
	"go_gateway/protobuf"
)

func buildInbound(t *testing.T, msgType string, fields map[int]string) *codec.Message {
	t.Helper()
	b := codec.NewBuilder(msgType, "TESTFIRM01", "FXPBRIDGE", 1)
	for tag, value := range fields {
		b.Set(tag, value)
	}
	msg, err := codec.NewParser(bytes.NewReader(b.Build())).ReadMessage()
	if err != nil {
		t.Fatalf("parse FIX message: %v", err)
	}
	return msg
}

func TestFixTimeInForceDefaults(t *testing.T) {
	newOrder, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeNewOrderSingle, map[int]string{
		codec.TagClOrdID: "N1", codec.TagSymbol: "AAPL", codec.TagSide: codec.SideBuy,
		codec.TagOrdType: codec.OrdTypeLimit, codec.TagOrderQty: "10", codec.TagPrice: "10.00",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := newOrder.GetNewOrder().TimeInForce; got != protobuf.TimeInForce(1) {
		t.Fatalf("absent NewOrderSingle tag 59 mapped to %d, want DAY", got)
	}

	replace, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderCancelReplaceRequest, map[int]string{
		codec.TagClOrdID: "N2", codec.TagOrigClOrdID: "N1", codec.TagSymbol: "AAPL",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := replace.GetOrderCancelReplace().NewTimeInForce; got != protobuf.TimeInForce(0) {
		t.Fatalf("absent cancel/replace tag 59 mapped to %d, want UNSPECIFIED", got)
	}
}

func TestFixEnumUnknownValuesAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		tag    int
		value  string
		msgType string
	}{
		{"side", codec.TagSide, "Z", codec.MsgTypeNewOrderSingle},
		{"order type", codec.TagOrdType, "Z", codec.MsgTypeNewOrderSingle},
		{"time in force", codec.TagTimeInForce, "Z", codec.MsgTypeNewOrderSingle},
	}
	base := map[int]string{
		codec.TagClOrdID: "N1", codec.TagSymbol: "AAPL", codec.TagSide: codec.SideBuy,
		codec.TagOrdType: codec.OrdTypeMarket, codec.TagOrderQty: "10",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := make(map[int]string, len(base)+1)
			for tag, value := range base {
				fields[tag] = value
			}
			fields[tc.tag] = tc.value
			if _, err := TranslateFIXtoFXP(buildInbound(t, tc.msgType, fields)); err == nil {
				t.Fatalf("unknown tag %d value %q was accepted", tc.tag, tc.value)
			}
		})
	}
	if _, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderCancelReplaceRequest, map[int]string{
		codec.TagClOrdID: "N2", codec.TagOrigClOrdID: "N1", codec.TagSymbol: "AAPL",
		codec.TagTimeInForce: "Z",
	})); err == nil {
		t.Fatal("unknown cancel/replace tag 59 value was accepted")
	}
}
