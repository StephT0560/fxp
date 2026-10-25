// SPDX-License-Identifier: MIT

package protobuf

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func tradeVectorReport() *TradeCaptureReport {
	return &TradeCaptureReport{
		Header:        &FXPHeader{ProtocolVersion: "2.5", SequenceNumber: 0},
		TradeId:       "EURUSD:7",
		Symbol:        "EURUSD",
		TradePrice:    108_250,
		TradeQuantity: 30,
		Timestamp:     &timestamppb.Timestamp{Seconds: 1_700_000_000, Nanos: 123_000_000},
		AggressorSide: OrderSide_SELL,
		Seq:           7,
		IsAuction:     true,
	}
}

func vectorPath(name string) string {
	return filepath.Join("..", "..", "tests", "vectors", name)
}

func TestTradeVectorGenerate(t *testing.T) {
	if os.Getenv("FXP_UPDATE_VECTORS") != "1" {
		t.Skip("set FXP_UPDATE_VECTORS=1 to regenerate wire vectors")
	}
	data, err := proto.Marshal(tradeVectorReport())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vectorPath("trade_capture_report_v2_5.hex"), []byte(hex.EncodeToString(data)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRustTradeVectorDecodesWithProtobufGo(t *testing.T) {
	encoded, err := os.ReadFile(vectorPath("trade_capture_report_v2_5.rust.hex"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	got := new(TradeCaptureReport)
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, tradeVectorReport()) {
		t.Fatalf("decoded report differs from fixed vector message:\n got: %v\nwant: %v", got, tradeVectorReport())
	}
}
