// SPDX-License-Identifier: MIT
package metrics

import (
	"context"
	"strings"
	"testing"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestAddressDefaultsAndInvalid(t *testing.T) {
	for _, value := range []string{"9091", ":9091", "localhost:9091"} {
		address, err := address(value)
		if err != nil || address != "127.0.0.1:9091" {
			t.Fatalf("%s: %s %v", value, address, err)
		}
	}
	for _, value := range []string{"bad", "host.example:90", "127.0.0.1:-1", "65536"} {
		if _, err := address(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if got, err := address("0.0.0.0:9091"); err != nil || got != "0.0.0.0:9091" {
		t.Fatal(got, err)
	}
}
func TestOptInAndFixedProtocolLabels(t *testing.T) {
	stop, err := Start(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if Enabled() {
		t.Fatal("metrics enabled by default")
	}
	enabled.Store(true)
	defer enabled.Store(false)
	before := clients.Load()
	done := Client()
	if clients.Load() != before+1 {
		t.Fatal("client not counted")
	}
	done()
	if clients.Load() != before {
		t.Fatal("client not released")
	}
	for _, m := range []*protobuf.FXPMessage{
		{Payload: &protobuf.FXPMessage_SessionControl{SessionControl: &protobuf.SessionControl{}}},
		{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderStatus: protobuf.OrderStatus_ORDER_REJECTED, RejectReason: "invalid private-order-123"}}},
	} {
		raw, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		Raw(raw, true)
		Raw(raw, false)
		Response(m)
	}
	if incoming[22].Load() != 1 || outgoing[8].Load() != 1 || rejects[6].Load() != 1 {
		t.Fatal("incorrect counts")
	}
	text := render()
	if strings.Contains(text, "private-order") || !strings.Contains(text, "type=\"session_control\"") {
		t.Fatal("unbounded label or missing type")
	}
	for _, raw := range [][]byte{nil, {255}, {128, 255, 1}, {0}} {
		Raw(raw, true)
	}
}
