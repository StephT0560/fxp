// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"io"
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

	other, peer := routedPipes(t)
	defer func() { _ = other.Close() }()
	defer func() { _ = peer.Close() }()
	key, err := registerStatusRoute(srv, req)
	if err != nil {
		t.Fatal(err)
	}
	req2 := proto.Clone(req).(*protobuf.OrderStatusRequest)
	req2.RequestId = "same"
	key2, err := registerStatusRoute(other, req2)
	if err != nil {
		t.Fatal(err)
	}
	if key == key2 {
		t.Fatal("same request id must not collide across sessions")
	}
	defer pendingRequests.get(key2, "TRADER", true)
	before := orderRoute{conn: other, owner: "TRADER"}
	orderClientMap.Store(namespacedOrderID("TRADER", "O1"), before)
	defer orderClientMap.Delete(namespacedOrderID("TRADER", "O1"))
	done := make(chan struct{})
	go func() {
		routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: key, Sender: "TRADER", Complete: true, Reports: []*protobuf.ExecutionReport{{OrderId: namespacedOrderID("TRADER", "O1"), OrderStatus: protobuf.OrderStatus_ORDER_FILLED}}}}})
		close(done)
	}()
	if err := cli.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
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

func TestStatusResponseRebindsLiveOrdersToTheRequester(t *testing.T) {
	srv, cli := routedPipes(t)
	stale, _ := pipes(t) // closed console: no client writer
	other, _ := routedPipes(t)
	live, done := namespacedOrderID("TRADER", "LIVE"), namespacedOrderID("TRADER", "DONE")
	busy, gone := namespacedOrderID("TRADER", "BUSY"), namespacedOrderID("TRADER", "GONE")
	orderClientMap.Store(live, orderRoute{conn: stale, owner: "TRADER"})
	orderClientMap.Store(done, orderRoute{conn: stale, owner: "TRADER"})
	orderClientMap.Store(busy, orderRoute{conn: other, owner: "TRADER"})
	for _, id := range []string{live, done, busy, gone} {
		defer orderClientMap.Delete(id)
	}
	key, err := registerStatusRoute(srv, &protobuf.OrderStatusRequest{RequestId: "R", Sender: "TRADER"})
	if err != nil {
		t.Fatal(err)
	}
	go routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: key, Sender: "TRADER", Complete: true, Reports: []*protobuf.ExecutionReport{
		{OrderId: live, OrderStatus: protobuf.OrderStatus_ORDER_NEW},
		{OrderId: done, OrderStatus: protobuf.OrderStatus_ORDER_FILLED},
		{OrderId: busy, OrderStatus: protobuf.OrderStatus_ORDER_NEW},
		{OrderId: gone, OrderStatus: protobuf.OrderStatus_ORDER_NEW},
	}}}})
	if got := readFXP(t, cli).GetOrderStatusResponse(); got == nil || got.Reports[0].OrderId != "LIVE" {
		t.Fatalf("response %+v", got)
	}
	if v, _ := orderClientMap.Load(live); v.(orderRoute).conn != srv {
		t.Fatal("live order not rebound to the requester")
	}
	if v, _ := orderClientMap.Load(done); v.(orderRoute).conn != stale {
		t.Fatal("terminal order route changed")
	}
	if v, _ := orderClientMap.Load(busy); v.(orderRoute).conn != other {
		t.Fatal("route on another open console was stolen")
	}
	if _, ok := orderClientMap.Load(gone); ok {
		t.Fatal("removed route recreated from a snapshot")
	}
}
