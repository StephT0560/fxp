// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
)

var codJTI atomic.Int64

// fakeCore points the pool at a listener and returns every frame it reads.
func fakeCore(t *testing.T) <-chan *protobuf.FXPMessage {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan *protobuf.FXPMessage, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				for {
					body, err := readFrame(c, 1<<20)
					if err != nil {
						return
					}
					m := &protobuf.FXPMessage{}
					if proto.Unmarshal(body, m) == nil {
						frames <- m
					}
				}
			}()
		}
	}()
	oldAddr, oldPool := coreAddr, corePool
	coreAddr = ln.Addr().String()
	pc, err := newPooledConn(0)
	if err != nil {
		t.Fatal(err)
	}
	corePool = make(chan *pooledConn, 1)
	corePool <- pc
	t.Cleanup(func() {
		_ = ln.Close()
		pc.shutdown()
		coreAddr, corePool = oldAddr, oldPool
	})
	return frames
}

func writeCODMsg(t *testing.T, c net.Conn, m *protobuf.FXPMessage) {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(frameBytes(raw)); err != nil {
		t.Fatal(err)
	}
}

func readCODMsg(t *testing.T, c net.Conn) *protobuf.FXPMessage {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr))
	if _, err := io.ReadFull(c, body); err != nil {
		t.Fatal(err)
	}
	m := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, m); err != nil {
		t.Fatal(err)
	}
	return m
}

// gatewaySession logs trader on to a gateway TCP handler over a pipe.
func gatewaySession(t *testing.T, trader string, cod bool) net.Conn {
	t.Helper()
	t.Setenv("FXP_JWT_SECRET", "cod-secret")
	now := time.Now().Unix()
	token := signJWT(t, "cod-secret", map[string]any{"alg": "HS256", "typ": "JWT"}, map[string]any{
		"sub": trader, "iat": now, "exp": now + 60, "iss": DefaultJWTIssuer,
		"aud": DefaultJWTAudience, "jti": fmt.Sprintf("cod-%d", codJTI.Add(1)),
	})
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleTCPClient(server)
		close(done)
	}()
	// The handler (and any cancel-on-disconnect it sends) must finish before
	// fakeCore restores the pool.
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	writeCODMsg(t, client, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{
		Logon: &protobuf.Logon{Token: token, CancelOnDisconnect: cod},
	}})
	if ack := readCODMsg(t, client).GetLogonAck(); ack == nil || !ack.Accepted {
		t.Fatalf("logon not accepted: %v", ack)
	}
	return client
}

func expectDisconnectCancel(t *testing.T, frames <-chan *protobuf.FXPMessage, trader string) {
	t.Helper()
	select {
	case m := <-frames:
		mc := m.GetOrderMassCancel()
		if mc == nil || mc.Sender != trader ||
			mc.Reason != protobuf.MassCancelReason_MASS_CANCEL_REASON_DISCONNECT ||
			mc.Symbol != "" || mc.Side != protobuf.OrderSide_ORDER_SIDE_UNSPECIFIED {
			t.Fatalf("expected owner-scoped disconnect mass cancel for %s, got %v", trader, m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cancel-on-disconnect request reached the core")
	}
}

func expectNothing(t *testing.T, frames <-chan *protobuf.FXPMessage) {
	t.Helper()
	select {
	case m := <-frames:
		t.Fatalf("unexpected frame to core: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCancelOnDisconnectOnAbruptClose(t *testing.T) {
	frames := fakeCore(t)
	client := gatewaySession(t, "COD-ABRUPT", true)
	_ = client.Close()
	expectDisconnectCancel(t, frames, "COD-ABRUPT")
	expectNothing(t, frames)
}

func TestCancelOnDisconnectOnLogout(t *testing.T) {
	frames := fakeCore(t)
	client := gatewaySession(t, "COD-LOGOUT", true)
	writeCODMsg(t, client, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logout{Logout: &protobuf.Logout{}}})
	expectDisconnectCancel(t, frames, "COD-LOGOUT")
	expectNothing(t, frames)
}

func TestNoCancelOnDisconnectByDefault(t *testing.T) {
	frames := fakeCore(t)
	client := gatewaySession(t, "NO-COD", false)
	_ = client.Close()
	expectNothing(t, frames)
}

func TestMassCancelIsBoundToTheCallerAndReportRoutedBack(t *testing.T) {
	frames := fakeCore(t)
	client := gatewaySession(t, "MC-OWNER", false)
	writeCODMsg(t, client, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancel{
		OrderMassCancel: &protobuf.OrderMassCancelRequest{Sender: "VICTIM", ClientRequestId: "MC1", Symbol: "AAPL"},
	}})
	var req *protobuf.OrderMassCancelRequest
	select {
	case m := <-frames:
		req = m.GetOrderMassCancel()
	case <-time.After(2 * time.Second):
		t.Fatal("mass cancel not forwarded")
	}
	if req == nil || req.Sender != "MC-OWNER" || req.Symbol != "AAPL" || req.ClientRequestId == "" || req.ClientRequestId == "MC1" {
		t.Fatalf("mass cancel must be rebound to the caller, got %v", req)
	}
	done := make(chan *protobuf.FXPMessage)
	go func() { done <- readCODMsg(t, client) }()
	routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{
		OrderMassCancelReport: &protobuf.OrderMassCancelReport{Sender: "MC-OWNER", ClientRequestId: req.ClientRequestId, TotalAffected: 3},
	}})
	rep := (<-done).GetOrderMassCancelReport()
	if rep == nil || rep.TotalAffected != 3 || rep.ClientRequestId != "MC1" {
		t.Fatalf("report not routed to requester: %v", rep)
	}
}

func TestBindOwnerScopesSTPGroupToTheTrader(t *testing.T) {
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{
		NewOrder: &protobuf.NewOrderSingle{OrderId: "O1", StpGroup: "desk"},
	}}
	bindOwner(msg, "T1")
	if g := msg.GetNewOrder().StpGroup; g != "2:T1::desk" {
		t.Fatalf("stp group = %q", g)
	}
	msg = &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "O2"}}}
	bindOwner(msg, "T1")
	if g := msg.GetNewOrder().StpGroup; g != "" {
		t.Fatalf("empty stp group must stay empty (owner scope), got %q", g)
	}
}

// Trader IDs may contain the separator, so "a" + "x::y" and "a::x" + "y"
// must still yield different groups.
func TestSTPGroupNamespaceIsUnambiguous(t *testing.T) {
	group := func(trader, g string) string {
		msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{
			NewOrder: &protobuf.NewOrderSingle{OrderId: "O", StpGroup: g},
		}}
		bindOwner(msg, trader)
		return msg.GetNewOrder().StpGroup
	}
	if a, b := group("a", "x::y"), group("a::x", "y"); a == b {
		t.Fatalf("groups of different traders collide: %q", a)
	}
}
