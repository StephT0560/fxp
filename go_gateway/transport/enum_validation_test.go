package transport

import (
	"testing"

	"go_gateway/protobuf"
)

func TestValidateOrderEnums(t *testing.T) {
	tests := []struct {
		name string
		msg  *protobuf.FXPMessage
		want string
	}{
		{"side unspecified", newOrder(0, 1, 1), "side is required"},
		{"side unknown", newOrder(99, 1, 1), "unknown side 99"},
		{"order type unspecified", newOrder(1, 0, 1), "order_type is required"},
		{"order type unknown", newOrder(1, 99, 1), "unknown order_type 99"},
		{"time in force unspecified", newOrder(1, 1, 0), "time_in_force is required"},
		{"time in force unknown", newOrder(1, 1, 99), "unknown time_in_force 99"},
		{"valid order", newOrder(1, 1, 1), ""},
		{"replace unspecified time in force", &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_OrderCancelReplace{OrderCancelReplace: &protobuf.OrderCancelReplaceRequest{}},
		}, ""},
		{"replace unknown time in force", &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_OrderCancelReplace{OrderCancelReplace: &protobuf.OrderCancelReplaceRequest{NewTimeInForce: 99}},
		}, "unknown new_time_in_force 99"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateOrderEnums(tc.msg); got != tc.want {
				t.Fatalf("validateOrderEnums() = %q, want %q", got, tc.want)
			}
		})
	}
}

func newOrder(side, orderType, timeInForce int32) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{
		OrderId: "enum-test", Symbol: "AAPL",
		Side: protobuf.OrderSide(side), OrderType: protobuf.OrderType(orderType),
		TimeInForce: protobuf.TimeInForce(timeInForce),
	}}}
}
