// SPDX-License-Identifier: MIT

package translator

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"fix_bridge/codec"
	"go_gateway/protobuf"
)

func newOrderWith(t *testing.T, extra map[int]string) (*protobuf.NewOrderSingle, error) {
	t.Helper()
	fields := map[int]string{
		codec.TagClOrdID: "S1", codec.TagSymbol: "AAPL", codec.TagSide: codec.SideBuy,
		codec.TagOrdType: codec.OrdTypeLimit, codec.TagOrderQty: "10", codec.TagPrice: "10.00",
	}
	for k, v := range extra {
		fields[k] = v
	}
	m, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeNewOrderSingle, fields))
	if err != nil {
		return nil, err
	}
	return m.GetNewOrder(), nil
}

func TestSelfMatchPreventionTags(t *testing.T) {
	cases := map[string]protobuf.SelfTradePrevention{
		"":   protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_UNSPECIFIED,
		"1":  protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_NEWEST,
		"2":  protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_OLDEST,
		"3":  protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_BOTH,
		"99": protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_DECREMENT_AND_CANCEL,
	}
	for v, want := range cases {
		extra := map[int]string{codec.TagSelfMatchPreventionID: "desk"}
		if v != "" {
			extra[codec.TagSelfMatchPreventionInstruction] = v
		}
		o, err := newOrderWith(t, extra)
		if err != nil {
			t.Fatalf("2964=%q: %v", v, err)
		}
		if o.StpMode != want || o.StpGroup != "desk" {
			t.Fatalf("2964=%q mapped to %v group %q", v, o.StpMode, o.StpGroup)
		}
	}
	if _, err := newOrderWith(t, map[int]string{codec.TagSelfMatchPreventionInstruction: "4"}); err == nil ||
		!strings.Contains(err.Error(), "SelfMatchPreventionInstruction (2964)") {
		t.Fatalf("unsupported 2964 value accepted: %v", err)
	}
}

func massCancel(t *testing.T, fields map[int]string) (*protobuf.OrderMassCancelRequest, error) {
	t.Helper()
	fields[codec.TagClOrdID] = "MC1"
	m, err := TranslateFIXtoFXP(buildInbound(t, codec.MsgTypeOrderMassCancelRequest, fields))
	if err != nil {
		return nil, err
	}
	return m.GetOrderMassCancel(), nil
}

func TestMassCancelRequestTypes(t *testing.T) {
	all, err := massCancel(t, map[int]string{codec.TagMassCancelRequestType: "7"})
	if err != nil || all.Symbol != "" || all.Side != protobuf.OrderSide_ORDER_SIDE_UNSPECIFIED ||
		all.ClientRequestId != "MC1" || all.Sender != "TESTFIRM01" ||
		all.Reason != protobuf.MassCancelReason_MASS_CANCEL_REASON_REQUEST {
		t.Fatalf("all orders: %v %v", all, err)
	}
	bySym, err := massCancel(t, map[int]string{codec.TagMassCancelRequestType: "1",
		codec.TagSymbol: "AAPL", codec.TagSide: codec.SideSell})
	if err != nil || bySym.Symbol != "AAPL" || bySym.Side != protobuf.OrderSide_SELL {
		t.Fatalf("by symbol: %v %v", bySym, err)
	}
	for _, tc := range []struct {
		fields     map[int]string
		reason     string
		textPrefix string
	}{
		{map[int]string{codec.TagMassCancelRequestType: "1"}, "1", "MassCancelRequestType=1 requires Symbol"},
		{map[int]string{codec.TagMassCancelRequestType: "3"}, "0", `unsupported MassCancelRequestType (530) "3"`},
	} {
		_, err := massCancel(t, tc.fields)
		var rej *MassCancelRejectError
		if !errors.As(err, &rej) || rej.Reason != tc.reason || !strings.HasPrefix(rej.Text, tc.textPrefix) {
			t.Fatalf("%v: got %v", tc.fields, err)
		}
	}
}

func parseOutbound(t *testing.T, raw []byte) *codec.Message {
	t.Helper()
	m, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMassCancelReportMapping(t *testing.T) {
	ok := parseOutbound(t, TranslateFXPtoFIX(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{
		OrderMassCancelReport: &protobuf.OrderMassCancelReport{ClientRequestId: "MC1", Symbol: "AAPL", TotalAffected: 2},
	}}, "FXPBRIDGE", "C", 5, nil))
	if ok.MsgType() != codec.MsgTypeOrderMassCancelReport || ok.Get(codec.TagClOrdID) != "MC1" ||
		ok.Get(codec.TagMassCancelRequestType) != "1" || ok.Get(codec.TagMassCancelResponse) != "1" ||
		ok.Get(codec.TagTotalAffectedOrders) != "2" || ok.Get(codec.TagMassCancelRejectReason) != "" {
		t.Fatalf("accepted report: %v", ok.Fields)
	}
	rej := parseOutbound(t, TranslateFXPtoFIX(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{
		OrderMassCancelReport: &protobuf.OrderMassCancelReport{ClientRequestId: "MC2", RejectReason: "invalid side"},
	}}, "FXPBRIDGE", "C", 6, nil))
	if rej.Get(codec.TagMassCancelRequestType) != "7" || rej.Get(codec.TagMassCancelResponse) != "0" ||
		rej.Get(codec.TagMassCancelRejectReason) != "99" || rej.Get(codec.TagText) != "invalid side" ||
		rej.Get(codec.TagTotalAffectedOrders) != "0" {
		t.Fatalf("rejected report: %v", rej.Fields)
	}
}

func TestRestatedExecType(t *testing.T) {
	if got, err := mapExecType(protobuf.ExecutionType_EXEC_RESTATED); err != nil || got != codec.ExecTypeRestated {
		t.Fatalf("EXEC_RESTATED -> %q, %v", got, err)
	}
}
