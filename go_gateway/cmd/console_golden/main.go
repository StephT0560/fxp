// SPDX-License-Identifier: MIT

// Command console_golden writes FXPMessage golden frames, encoded by the Go
// gateway's protobuf package, for the web console's decoder tests:
//
//	go run ./cmd/console_golden ../web/src/testdata/golden
package main

import (
	"log"
	"os"
	"path/filepath"

	"go_gateway/protobuf"

	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: console_golden <out-dir>")
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	ts := &timestamppb.Timestamp{Seconds: 1_700_000_000, Nanos: 123_000_000}
	hdr := &protobuf.FXPHeader{ProtocolVersion: "2.1", SequenceNumber: 7}
	frames := map[string]*protobuf.FXPMessage{
		"logon":     {Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: "fixture-not-a-credential", SessionId: "fixture-session", Timestamp: ts}}},
		"new_order": {Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "fixture-order", ClientOrderId: "fixture-order", Symbol: "DEMO", Side: protobuf.OrderSide_BUY, Quantity: 10, OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_DAY, Price: 12345, Timestamp: ts}}},
		"session_status": {Payload: &protobuf.FXPMessage_TradingSessionStatus{TradingSessionStatus: &protobuf.TradingSessionStatus{
			Symbol: "AAPL", Phase: protobuf.TradingPhase_OPENING_CALL, Timestamp: ts,
		}}},
		"snapshot": {Payload: &protobuf.FXPMessage_MarketDataSnapshot{MarketDataSnapshot: &protobuf.MarketDataSnapshot{
			Header: hdr, Symbol: "AAPL",
			BidLevels: []*protobuf.MarketOrderBookEntry{
				{Price: 15000, Size: 300, Side: protobuf.OrderSide_BUY},
				{Price: 14999, Size: 100, Side: protobuf.OrderSide_BUY},
			},
			AskLevels: []*protobuf.MarketOrderBookEntry{
				{Price: 15002, Size: 200, Side: protobuf.OrderSide_SELL},
			},
			BidAskSpread: 2, TotalVolume: 600, Timestamp: ts,
		}}},
		"incremental": {Payload: &protobuf.FXPMessage_MarketDataIncremental{MarketDataIncremental: &protobuf.MarketDataIncrementalUpdate{
			Header: hdr, Symbol: "AAPL", Timestamp: ts,
			Changes: []*protobuf.MarketOrderUpdate{
				{UpdateType: 0, Price: 15001, Quantity: 50, Side: protobuf.OrderSide_BUY},
				{UpdateType: 1, Price: 15002, Quantity: 20, Side: protobuf.OrderSide_SELL},
				{UpdateType: 2, Price: 14999, Quantity: 100, Side: protobuf.OrderSide_BUY},
			},
		}}},
		// Price and seq exceed 2^53 to prove int64/uint64 decode as exact bigint.
		"trade": {Payload: &protobuf.FXPMessage_TradeCaptureReport{TradeCaptureReport: &protobuf.TradeCaptureReport{
			Header: hdr, TradeId: "T-42", Symbol: "BTC/USD", TradePrice: 9_007_199_254_740_993,
			TradeQuantity: 3, Timestamp: ts, AggressorSide: protobuf.OrderSide_SELL,
			Seq: 18_446_744_073_709_551_615, IsAuction: false,
		}}},
		"auction_info": {Payload: &protobuf.FXPMessage_AuctionInfo{AuctionInfo: &protobuf.AuctionInfo{
			Symbol: "AAPL", Phase: protobuf.TradingPhase_CLOSING_CALL, IndicativePrice: 15001,
			IndicativeVolume: 250, ImbalanceQuantity: 40, ImbalanceSide: protobuf.OrderSide_BUY,
			ReferencePrice: 15000, Timestamp: ts,
		}}},
	}
	for name, msg := range frames {
		b, err := gproto.MarshalOptions{Deterministic: true}.Marshal(msg)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name+".bin"), b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
