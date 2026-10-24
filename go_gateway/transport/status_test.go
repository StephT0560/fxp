// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"testing"
	"time"
)

func TestStatusOwnerBindingAndIndependentRoutes(t *testing.T) {
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusRequest{OrderStatusRequest: &protobuf.OrderStatusRequest{RequestId: "same", Sender: "VICTIM", OrderId: "O1"}}}
	bindOwner(msg, "TRADER")
	req := msg.GetOrderStatusRequest()
	if req.Sender != "TRADER" || req.OrderId != namespacedOrderID("TRADER", "O1") {
		t.Fatalf("unbound %+v", req)
	}
	srv, cli := routedPipes(t)
	defer srv.Close()
	defer cli.Close()
	other, peer := net.Pipe()
	defer other.Close()
	defer peer.Close()
	key := registerStatusRoute(srv, req)
	req2 := proto.Clone(req).(*protobuf.OrderStatusRequest)
	req2.RequestId = "same"
	key2 := registerStatusRoute(other, req2)
	if key == key2 {
		t.Fatal("same request id must not collide across sessions")
	}
	defer removeStatusRoute(key2)
	before := orderRoute{conn: other, owner: "TRADER"}
	orderClientMap.Store(namespacedOrderID("TRADER", "O1"), before)
	defer orderClientMap.Delete(namespacedOrderID("TRADER", "O1"))
	done := make(chan struct{})
	go func() {
		routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: key, Sender: "TRADER", Complete: true, Reports: []*protobuf.ExecutionReport{{OrderId: namespacedOrderID("TRADER", "O1"), OrderStatus: protobuf.OrderStatus_ORDER_FILLED}}}}})
		close(done)
	}()
	cli.SetReadDeadline(time.Now().Add(time.Second))
	var h [4]byte
	if _, err := io.ReadFull(cli, h[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint32(h[:]))
	if _, err := io.ReadFull(cli, body); err != nil {
		t.Fatal(err)
	}
	var got protobuf.FXPMessage
	if err := proto.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	response := got.GetOrderStatusResponse()
	if response.RequestId != "same" || response.Reports[0].OrderId != "O1" {
		t.Fatalf("bad response %+v", response)
	}
	<-done
	route, _ := orderClientMap.Load(namespacedOrderID("TRADER", "O1"))
	if route.(orderRoute).conn != other {
		t.Fatal("status request hijacked live route")
	}
}
