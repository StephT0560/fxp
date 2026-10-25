// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"net"
	"runtime"
	"testing"
	"time"
	"weak"
)

func readPendingMessage(t *testing.T, conn interface {
	Read([]byte) (int, error)
	SetReadDeadline(time.Time) error
}) *protobuf.FXPMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, err := readFrame(conn, 1_048_576)
	if err != nil {
		t.Fatal(err)
	}
	m := &protobuf.FXPMessage{}
	if err = proto.Unmarshal(raw, m); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestMassCancelTimeoutRemovesRouteAndNotifies(t *testing.T) {
	srv, cli := routedPipes(t)
	req := &protobuf.OrderMassCancelRequest{Sender: "T", ClientRequestId: "same", Symbol: "MSFT", Side: protobuf.OrderSide_BUY}
	key, err := registerMassCancelRoute(srv, req, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	rep := readPendingMessage(t, cli).GetOrderMassCancelReport()
	if rep == nil || rep.ClientRequestId != "same" || rep.Sender != "T" || rep.RejectReason != "mass cancel timeout" {
		t.Fatalf("bad timeout: %v", rep)
	}
	if pendingRequests.get(key, "T", false) != nil {
		t.Fatal("expired route retained connection")
	}
}
func TestDisconnectStopsTimersAndRemovesBothKinds(t *testing.T) {
	srv, _ := routedPipes(t)
	key, err := registerMassCancelRoute(srv, &protobuf.OrderMassCancelRequest{Sender: "T", ClientRequestId: "M"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	status, err := registerStatusRoute(srv, &protobuf.OrderStatusRequest{Sender: "T", RequestId: "S"})
	if err != nil {
		t.Fatal(err)
	}
	entry := pendingRequests.get(key, "T", false)
	unregisterClientWriter(srv)
	if pendingRequests.get(key, "T", false) != nil || pendingRequests.get(status, "T", false) != nil {
		t.Fatal("closed connection retained")
	}
	if entry.timer.Stop() {
		t.Fatal("disconnect did not stop timer")
	}
}
func TestPendingCapAndLateResponseIsolation(t *testing.T) {
	tracker := newPendingTracker(1)
	key, err := tracker.add(&pendingRequest{conn: "one", owner: "T", kind: "test"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tracker.add(&pendingRequest{conn: "two", owner: "T", kind: "test"}, time.Minute); err == nil {
		t.Fatal("pending cap ignored")
	}
	if tracker.get(key, "WRONG", true) != nil {
		t.Fatal("owner bypass")
	}
	tracker.clear("one")
	srv, cli := routedPipes(t)
	req := &protobuf.OrderMassCancelRequest{Sender: "T", ClientRequestId: "same"}
	old, err := registerMassCancelRoute(srv, req, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	readPendingMessage(t, cli)
	current, err := registerMassCancelRoute(srv, req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{OrderMassCancelReport: &protobuf.OrderMassCancelReport{Sender: "T", ClientRequestId: old, TotalAffected: 99}}})
	if pendingRequests.get(current, "T", false) == nil {
		t.Fatal("late reply consumed new request")
	}
	routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{OrderMassCancelReport: &protobuf.OrderMassCancelReport{Sender: "T", ClientRequestId: current, TotalAffected: 2}}})
	rep := readPendingMessage(t, cli).GetOrderMassCancelReport()
	if rep.ClientRequestId != "same" || rep.TotalAffected != 2 {
		t.Fatalf("wrong response: %v", rep)
	}
}
func TestPendingDisconnectChurnLeavesNoWorkersOrRoutes(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		srv, cli := pipes(t)
		writer := registerClientWriter(srv)
		for j := 0; j < 4; j++ {
			if _, err := registerMassCancelRoute(srv, &protobuf.OrderMassCancelRequest{Sender: "T"}, time.Hour); err != nil {
				t.Fatal(err)
			}
		}
		unregisterClientWriter(srv)
		_ = cli.Close()
		<-writer.finished
	}
	if pendingRequests.count() != 0 {
		t.Fatal("pending routes leaked after disconnect churn")
	}
	runtime.GC()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+4 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+4 {
		t.Fatalf("goroutines grew from %d to %d", before, after)
	}
}

type weakPendingConn struct{ net.Conn }

func closedPendingReference(t *testing.T) weak.Pointer[weakPendingConn] {
	t.Helper()
	srv, cli := net.Pipe()
	wrapped := &weakPendingConn{srv}
	writer := registerClientWriter(wrapped)
	if _, err := registerMassCancelRoute(wrapped, &protobuf.OrderMassCancelRequest{Sender: "T"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	reference := weak.Make(wrapped)
	unregisterClientWriter(wrapped)
	_ = cli.Close()
	<-writer.finished
	return reference
}
func TestPendingTimerDoesNotRetainDisconnectedConnection(t *testing.T) {
	reference := closedPendingReference(t)
	deadline := time.Now().Add(2 * time.Second)
	for reference.Value() != nil && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	if reference.Value() != nil {
		t.Fatal("stopped pending timer retains the connection")
	}
}
func TestMassCancelTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value   string
		seconds int
	}{{"", 30}, {"2", 2}, {"0", 30}, {"invalid", 30}, {"86401", 30}} {
		t.Setenv("FXP_GW_MASS_CANCEL_TIMEOUT_SEC", tc.value)
		if got := massCancelTimeout(); got != time.Duration(tc.seconds)*time.Second {
			t.Fatalf("timeout=%v", got)
		}
	}
}
