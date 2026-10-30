// SPDX-License-Identifier: MIT
// Independent Go protobuf encoder for the Python SDK's length-prefixed goldens.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	pb "go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: python_golden <directory>")
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0755); err != nil {
		log.Fatal(err)
	}
	zero := int32(0)
	replace := func(display *int32) *pb.FXPMessage {
		return &pb.FXPMessage{Payload: &pb.FXPMessage_OrderCancelReplace{OrderCancelReplace: &pb.OrderCancelReplaceRequest{OrderId: "N", NewOrderId: "R", Symbol: "TEST", ClientReplaceId: "replace-N", NewPrice: 12346, NewQuantity: 7, NewDisplayQuantity: display}}}
	}
	frames := map[string]*pb.FXPMessage{
		"logon":        {Payload: &pb.FXPMessage_Logon{Logon: &pb.Logon{Token: "fixture-not-a-credential", CancelOnDisconnect: true}}},
		"ack":          {Payload: &pb.FXPMessage_LogonAck{LogonAck: &pb.LogonAck{Accepted: true}}},
		"new":          {Payload: &pb.FXPMessage_NewOrder{NewOrder: &pb.NewOrderSingle{OrderId: "N", Symbol: "TEST", Side: pb.OrderSide_BUY, Quantity: 10, Price: 12345, OrderType: pb.OrderType_LIMIT, TimeInForce: pb.TimeInForce_GTC, ClientOrderId: "client-N"}}},
		"cancel":       {Payload: &pb.FXPMessage_OrderCancel{OrderCancel: &pb.OrderCancelRequest{OrderId: "N", Symbol: "TEST", ClientCancelId: "cancel-N"}}},
		"replace-zero": replace(&zero), "replace-absent": replace(nil),
		"mass-cancel": {Payload: &pb.FXPMessage_OrderMassCancel{OrderMassCancel: &pb.OrderMassCancelRequest{Symbol: "TEST", ClientRequestId: "mass-N", Reason: pb.MassCancelReason_MASS_CANCEL_REASON_REQUEST}}},
		"subscribe":   {Payload: &pb.FXPMessage_MarketDataRequest{MarketDataRequest: &pb.MarketDataRequest{Symbols: []string{"TEST"}}}},
		"fill":        {Payload: &pb.FXPMessage_ExecutionReport{ExecutionReport: &pb.ExecutionReport{OrderId: "N", Symbol: "TEST", ExecutionType: pb.ExecutionType_EXEC_FILLED, OrderStatus: pb.OrderStatus_ORDER_PARTIALLY_FILLED, ExecutedQuantity: 7, ExecutionPrice: 12345, LeavesQuantity: 3, CumQuantity: 7, OrderQuantity: 10}}},
		"reject":      {Payload: &pb.FXPMessage_ExecutionReport{ExecutionReport: &pb.ExecutionReport{OrderId: "N", Symbol: "TEST", ExecutionType: pb.ExecutionType_EXEC_REJECTED, RejectReason: "quantity must be positive"}}},
		"trade":       {Payload: &pb.FXPMessage_TradeCaptureReport{TradeCaptureReport: &pb.TradeCaptureReport{Symbol: "TEST", TradePrice: 9007199254740993, TradeQuantity: 7, Seq: 18446744073709551615}}},
		"signed":      {Payload: &pb.FXPMessage_NewOrder{NewOrder: &pb.NewOrderSingle{OrderId: "invalid", Price: -1, Quantity: -1}}},
	}
	type vector struct {
		Kind   string         `json:"kind"`
		Fields map[string]any `json:"fields"`
	}
	fields := map[string]vector{
		"logon":          {"logon", map[string]any{"token": "fixture-not-a-credential", "cancel_on_disconnect": true}},
		"ack":            {"logon_ack", map[string]any{"accepted": true}},
		"new":            {"new_order", map[string]any{"order_id": "N", "price": 12345, "quantity": 10, "side": 1, "client_order_id": "client-N"}},
		"cancel":         {"order_cancel", map[string]any{"order_id": "N", "client_cancel_id": "cancel-N"}},
		"replace-zero":   {"order_cancel_replace", map[string]any{"new_order_id": "R", "new_quantity": 7, "new_display_quantity": 0}},
		"replace-absent": {"order_cancel_replace", map[string]any{"new_order_id": "R", "new_quantity": 7}},
		"mass-cancel":    {"order_mass_cancel", map[string]any{"client_request_id": "mass-N", "reason": 1}},
		"subscribe":      {"market_data_request", map[string]any{}},
		"fill":           {"execution_report", map[string]any{"executed_quantity": 7, "leaves_quantity": 3, "cum_quantity": 7, "order_quantity": 10}},
		"reject":         {"execution_report", map[string]any{"reject_reason": "quantity must be positive"}},
		"trade":          {"trade_capture_report", map[string]any{"trade_price": int64(9007199254740993), "seq": uint64(18446744073709551615)}},
		"signed":         {"new_order", map[string]any{"price": -1, "quantity": -1}},
	}
	for name, msg := range frames {
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
		if err != nil {
			log.Fatal(err)
		}
		frame := make([]byte, 4+len(raw))
		binary.BigEndian.PutUint32(frame, uint32(len(raw)))
		copy(frame[4:], raw)
		if err := os.WriteFile(filepath.Join(out, name+".bin"), frame, 0644); err != nil {
			log.Fatal(err)
		}
	}
	raw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "fields.json"), append(raw, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
	// Fixed JWT bytes independently signed by Go's HMAC implementation.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT","kid":"key"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"local-trader","iss":"test","aud":"gateway","iat":1700000000,"nbf":1700000000,"exp":1700000060,"jti":"test-jti"}`))
	input := header + "." + claims
	mac := hmac.New(sha256.New, []byte(strings.Repeat("a", 32)))
	if _, err := mac.Write([]byte(input)); err != nil {
		log.Fatal(err)
	}
	token := input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "\n"
	if err := os.WriteFile(filepath.Join(out, "dev-token.txt"), []byte(token), 0644); err != nil {
		log.Fatal(err)
	}

	// Independent gallery operator-role vector, with a fixed clock/JTI.
	operatorClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub": "demo-operator", "iss": "test", "aud": "gateway", "iat": 1700000000, "nbf": 1700000000, "exp": 1700000900, "jti": "test-jti", "fxp_role": "operator"}`))
	operatorInput := header + "." + operatorClaims
	operatorMAC := hmac.New(sha256.New, []byte(strings.Repeat("a", 32)))
	if _, err := operatorMAC.Write([]byte(operatorInput)); err != nil {
		log.Fatal(err)
	}
	operatorToken := operatorInput + "." + base64.RawURLEncoding.EncodeToString(operatorMAC.Sum(nil)) + "\n"
	if err := os.WriteFile(filepath.Join(out, "operator-token.txt"), []byte(operatorToken), 0644); err != nil {
		log.Fatal(err)
	}

}
