// SPDX-License-Identifier: MIT

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

func TestGTDExpireDateTranslation(t *testing.T) {
	for _, date := range []string{"20240229", "20270131"} {
		msg, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeNewOrderSingle, map[int]string{
			codec.TagClOrdID: "GTD", codec.TagSymbol: "AAPL", codec.TagSide: codec.SideBuy,
			codec.TagOrdType: codec.OrdTypeLimit, codec.TagOrderQty: "10", codec.TagPrice: "10.00",
			codec.TagTimeInForce: codec.TIFGTD, codec.TagExpireDate: date,
			codec.TagTransactTime: "20260101-12:00:00.000",
		}))
		if err != nil {
			t.Fatal(err)
		}
		order := msg.GetNewOrder()
		want := date[:4] + "-" + date[4:6] + "-" + date[6:]
		if order.TimeInForce != protobuf.TimeInForce_GTD || order.ExpireDate != want {
			t.Fatalf("got TIF=%v date=%q, want GTD %q", order.TimeInForce, order.ExpireDate, want)
		}
	}
	for _, date := range []string{"", "20250229", "2026011", "2026-01-01", "20261301", "00000101"} {
		msg, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeNewOrderSingle, map[int]string{
			codec.TagClOrdID: "BAD", codec.TagSymbol: "AAPL", codec.TagSide: codec.SideBuy,
			codec.TagOrdType: codec.OrdTypeLimit, codec.TagOrderQty: "10", codec.TagPrice: "10.00",
			codec.TagTimeInForce: codec.TIFGTD, codec.TagExpireDate: date,
			codec.TagTransactTime: "20260101-12:00:00.000",
		}))
		if date == "" {
			if err != nil {
				t.Fatal(err)
			}
			if order := msg.GetNewOrder(); order.ExpireDate != "" || order.TimeInForce != protobuf.TimeInForce_GTD {
				t.Fatalf("missing date must reach the core as GTD without expire_date: %v", order)
			}
		} else if err == nil {
			t.Fatalf("malformed date %q was accepted", date)
		}
	}
}

func TestFixEnumUnknownValuesAreRejected(t *testing.T) {
	cases := []struct {
		name    string
		tag     int
		value   string
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

func TestParseFixedPoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"two decimal places", "185.42", 18542, false},
		{"whole number", "185", 18500, false},
		{"one decimal place", "185.4", 18540, false},
		{"round up", "1.005", 101, false},
		{"round down", "1.004", 100, false},
		{"round to next dollar", "0.999", 100, false},
		{"negative sub-unit", "-0.50", -50, false},
		{"negative half cent", "-0.005", -1, false},
		{"negative half away from zero", "-1.235", -124, false},
		{"empty", "", 0, true},
		{"sign only", "-", 0, true},
		{"garbage", "abc", 0, true},
		{"non-digit decimal", "1.2x", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFixedPoint(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseFixedPoint(%q) succeeded with %d", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFixedPoint(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseFixedPoint(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestCancelReplaceStopPx(t *testing.T) {
	replace, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderCancelReplaceRequest, map[int]string{
		codec.TagClOrdID: "N2", codec.TagOrigClOrdID: "N1", codec.TagSymbol: "AAPL", codec.TagStopPx: "101.50",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := replace.GetOrderCancelReplace().NewStopPrice; got != 10150 {
		t.Fatalf("NewStopPrice = %d, want 10150", got)
	}
	if _, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderCancelReplaceRequest, map[int]string{
		codec.TagClOrdID: "N2", codec.TagOrigClOrdID: "N1", codec.TagSymbol: "AAPL", codec.TagStopPx: "-1",
	})); err == nil {
		t.Fatal("negative StopPx accepted")
	}
}
