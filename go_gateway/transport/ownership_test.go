package transport

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
)

func TestBindOwnerStampsSenderAndNamespacesIDs(t *testing.T) {
	cancel := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderCancel{
		OrderCancel: &protobuf.OrderCancelRequest{OrderId: "VIC-ORD-1", Sender: "VICTIM"},
	}}
	bindOwner(cancel, "ATTACKER")
	c := cancel.GetOrderCancel()
	if c.Sender != "ATTACKER" {
		t.Fatalf("client-supplied sender not overridden: %q", c.Sender)
	}
	if c.OrderId != "ATTACKER::VIC-ORD-1" {
		t.Fatalf("cancel not namespaced to requester: %q", c.OrderId)
	}

	order := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{
		NewOrder: &protobuf.NewOrderSingle{OrderId: "VIC-ORD-1"},
	}}
	bindOwner(order, "VICTIM")
	if got := order.GetNewOrder().OrderId; got == c.OrderId {
		t.Fatalf("same ClOrdID from two traders collided: %q", got)
	}

	cr := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderCancelReplace{
		OrderCancelReplace: &protobuf.OrderCancelReplaceRequest{OrderId: "A", NewOrderId: "B"},
	}}
	bindOwner(cr, "T1")
	r := cr.GetOrderCancelReplace()
	if r.OrderId != "T1::A" || r.NewOrderId != "T1::B" || r.Sender != "T1" {
		t.Fatalf("replace not bound: %+v", r)
	}
}

// A report is delivered only to the route registered under the owner's
// namespaced ID, with the namespace stripped.
func TestRouteFromCoreUsesOwnerNamespace(t *testing.T) {
	victimSrv, victimCli := net.Pipe()
	defer victimSrv.Close()
	defer victimCli.Close()

	orderClientMap.Store("VICTIM::ORD-1", orderRoute{conn: victimSrv, owner: "VICTIM"})
	defer orderClientMap.Delete("VICTIM::ORD-1")

	// Report for the attacker's namespace has no route: it must not reach the victim.
	routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
		ExecutionReport: &protobuf.ExecutionReport{OrderId: "ATTACKER::ORD-1",
			OrderStatus: protobuf.OrderStatus_ORDER_REJECTED},
	}})

	go routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
		ExecutionReport: &protobuf.ExecutionReport{OrderId: "VICTIM::ORD-1",
			ExecutionId: "EXEC_NEW-VICTIM::ORD-1-", OrderStatus: protobuf.OrderStatus_ORDER_NEW},
	}})

	_ = victimCli.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(victimCli, hdr[:]); err != nil {
		t.Fatalf("victim report not delivered: %v", err)
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(victimCli, body); err != nil {
		t.Fatal(err)
	}
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, msg); err != nil {
		t.Fatal(err)
	}
	rep := msg.GetExecutionReport()
	if rep.OrderId != "ORD-1" || rep.ExecutionId != "EXEC_NEW-ORD-1-" {
		t.Fatalf("namespace not stripped: order_id=%q exec_id=%q", rep.OrderId, rep.ExecutionId)
	}
}
