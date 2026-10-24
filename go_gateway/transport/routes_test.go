// SPDX-License-Identifier: MIT

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

func readReport(t *testing.T, c net.Conn) *protobuf.ExecutionReport {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		t.Fatalf("report not delivered: %v", err)
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(c, body); err != nil {
		t.Fatal(err)
	}
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, msg); err != nil {
		t.Fatal(err)
	}
	return msg.GetExecutionReport()
}

func route(rep *protobuf.ExecutionReport) chan struct{} {
	done := make(chan struct{})
	go func() {
		routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: rep}})
		close(done)
	}()
	return done
}

func requestReject(id string) *protobuf.ExecutionReport {
	return &protobuf.ExecutionReport{OrderId: id, OrigOrderId: id,
		ExecutionType: protobuf.ExecutionType_EXEC_REJECTED,
		OrderStatus:   protobuf.OrderStatus_ORDER_REJECTED}
}

func connOf(id string) any {
	v, ok := orderClientMap.Load(id)
	if !ok {
		return nil
	}
	return v.(orderRoute).conn
}

func pipes(t *testing.T) (net.Conn, net.Conn) {
	s, c := net.Pipe()
	t.Cleanup(func() { s.Close(); c.Close() })
	return s, c
}

func routedPipes(t *testing.T) (net.Conn, net.Conn) {
	s, c := pipes(t)
	writer := registerClientWriter(s)
	t.Cleanup(func() {
		unregisterClientWriter(s)
		<-writer.finished
	})
	return s, c
}

// A rejected cancel (e.g. from a second session of the same trader) must not
// steal or delete the live order's route.
func TestRejectedCancelKeepsLiveRoute(t *testing.T) {
	s1, c1 := routedPipes(t)
	s2, _ := routedPipes(t)
	orderClientMap.Store("T::A", orderRoute{conn: s1, owner: "T"})
	defer orderClientMap.Delete("T::A")

	if registerRoute("T::A", orderRoute{conn: s2, owner: "T", provisional: true}) {
		t.Fatal("cancel request overwrote the live order's route")
	}
	done := route(requestReject("T::A"))
	if rep := readReport(t, c1); rep.OrderId != "A" {
		t.Fatalf("reject delivered for %q", rep.OrderId)
	}
	<-done
	if connOf("T::A") != s1 {
		t.Fatal("live order route removed or changed by a rejected cancel")
	}
}

// A rejected replace keeps the original's route, drops the provisional
// replacement route and never re-points another live order's route.
func TestRejectedReplaceKeepsRoutes(t *testing.T) {
	s1, c1 := routedPipes(t)
	s2, _ := routedPipes(t)
	orderClientMap.Store("T::A", orderRoute{conn: s1, owner: "T"})
	orderClientMap.Store("T::B", orderRoute{conn: s2, owner: "T"})
	defer orderClientMap.Delete("T::A")
	defer orderClientMap.Delete("T::B")

	// Replace A -> B (duplicate ID): B's live route must not be overwritten.
	if registerRoute("T::B", orderRoute{conn: s1, owner: "T", provisional: true}) {
		t.Fatal("replacement ID overwrote another live order's route")
	}
	// Replace A -> C (rejected, e.g. qty <= filled).
	if !registerRoute("T::C", orderRoute{conn: s1, owner: "T", provisional: true}) {
		t.Fatal("provisional replacement route not registered")
	}
	pendingReplace.Store("T::A", "T::C")

	done := route(requestReject("T::A"))
	readReport(t, c1)
	<-done
	if connOf("T::A") != s1 || connOf("T::B") != s2 {
		t.Fatal("rejected replace changed live routes")
	}
	if connOf("T::C") != nil {
		t.Fatal("provisional replacement route leaked after reject")
	}
	if _, ok := pendingReplace.Load("T::A"); ok {
		t.Fatal("pending replace not cleared")
	}
}

// Reject of a request for an order with no route only cleans up its own
// provisional route; a live report promotes a provisional route.
func TestProvisionalRouteLifecycle(t *testing.T) {
	s1, c1 := routedPipes(t)
	registerRoute("T::X", orderRoute{conn: s1, owner: "T", provisional: true})
	done := route(requestReject("T::X"))
	readReport(t, c1)
	<-done
	if connOf("T::X") != nil {
		t.Fatal("provisional route for unknown order not removed")
	}

	registerRoute("T::Y", orderRoute{conn: s1, owner: "T", provisional: true})
	defer orderClientMap.Delete("T::Y")
	done = route(&protobuf.ExecutionReport{OrderId: "T::Y", OrderStatus: protobuf.OrderStatus_ORDER_NEW})
	readReport(t, c1)
	<-done
	v, ok := orderClientMap.Load("T::Y")
	if !ok || v.(orderRoute).provisional {
		t.Fatal("provisional route not promoted by a live report")
	}
}

func TestInvalidNewOrderIsRejectedWithoutForwarding(t *testing.T) {
	client, peer := routedPipes(t)
	core, corePeer := net.Pipe()
	oldPool := corePool
	corePool = make(chan *pooledConn, 1)
	corePool <- newPooledConnWith(0, core)
	t.Cleanup(func() {
		corePool = oldPool
		core.Close()
		corePeer.Close()
	})

	coreBytes := make(chan []byte, 1)
	go func() {
		_ = corePeer.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(corePeer, buf); err == nil {
			n := binary.BigEndian.Uint32(buf)
			body := make([]byte, n)
			if _, err := io.ReadFull(corePeer, body); err == nil {
				coreBytes <- body
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		HandleFXPMessage(client, newOrder(0, 1, 1))
		close(done)
	}()

	rep := readReport(t, peer)
	if rep.OrderId != "enum-test" || rep.ExecutionType != protobuf.ExecutionType_EXEC_REJECTED ||
		rep.OrderStatus != protobuf.OrderStatus_ORDER_REJECTED || rep.RejectReason != "side is required" ||
		rep.Symbol != "AAPL" || rep.Side != 0 {
		t.Fatalf("unexpected enum reject: %+v", rep)
	}
	select {
	case body := <-coreBytes:
		t.Fatalf("invalid NewOrder reached core: %x", body)
	case <-time.After(300 * time.Millisecond):
	}
	<-done
	if _, ok := orderClientMap.Load("enum-test"); ok {
		t.Fatal("invalid order route was registered")
	}
}

// A cancel from a second session of the same trader: its reject goes only to
// the requester; a successful cancel reaches both the requester and the
// session that placed the order. Fills keep the original route.
func TestRequestRepliesReachRequester(t *testing.T) {
	s1, c1 := routedPipes(t)
	s2, c2 := routedPipes(t)
	orderClientMap.Store("T::R", orderRoute{conn: s1, owner: "T"})
	defer orderClientMap.Delete("T::R")

	registerRequest("T::R", s2, "T")
	done := route(requestReject("T::R"))
	if rep := readReport(t, c2); rep.OrderId != "R" {
		t.Fatalf("requester got %q", rep.OrderId)
	}
	select {
	case <-done: // would block forever if it also wrote to the original session
	case <-time.After(2 * time.Second):
		t.Fatal("reject was also sent to the original session")
	}
	if connOf("T::R") != s1 {
		t.Fatal("request changed the live route")
	}

	registerRequest("T::R", s2, "T")
	done = route(&protobuf.ExecutionReport{OrderId: "T::R",
		ExecutionType: protobuf.ExecutionType_EXEC_CANCELED,
		OrderStatus:   protobuf.OrderStatus_ORDER_CANCELED})
	readReport(t, c1)
	readReport(t, c2)
	<-done
	if _, ok := inflightRequests.Load("T::R"); ok {
		t.Fatal("in-flight request not cleared")
	}
}
