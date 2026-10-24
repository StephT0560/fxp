// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"fix_bridge/codec"
)

func mdRequest(reqID, subType string, symbols ...string) *codec.Message {
	m := &codec.Message{Fields: map[int]string{
		codec.TagMsgType: codec.MsgTypeMarketDataRequest, codec.TagMDReqID: reqID,
		codec.TagSubscriptionRequestType: subType,
	}}
	for _, s := range symbols {
		m.Tags = append(m.Tags, codec.Field{Tag: codec.TagSymbol, Value: s})
	}
	return m
}

func TestMDSubscriptionsTrackMDReqID(t *testing.T) {
	var s mdSubscriptions
	if !s.track(mdRequest("R1", "1", "AAPL", "MSFT")) {
		t.Fatal("subscribe must be forwarded")
	}
	if s.reqID("AAPL") != "R1" || s.reqID("MSFT") != "R1" || s.reqID("IBM") != "" {
		t.Fatalf("reqIDs = %q %q %q", s.reqID("AAPL"), s.reqID("MSFT"), s.reqID("IBM"))
	}
	if s.track(mdRequest("R1", "2", "AAPL")) {
		t.Fatal("unsubscribe must not be forwarded")
	}
	if s.reqID("AAPL") != "" || s.reqID("MSFT") != "R1" {
		t.Fatalf("after unsubscribe: %q %q", s.reqID("AAPL"), s.reqID("MSFT"))
	}
}
