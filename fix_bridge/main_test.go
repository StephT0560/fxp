// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"fix_bridge/codec"
	"fix_bridge/gateway"
	"fix_bridge/session"
	"go_gateway/protobuf"
)

func TestAcceptFIXClientsConnectionLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan struct{})
	go func() {
		acceptFIXClients(listener, 1, func(conn net.Conn) {
			accepted <- conn
			<-release
		})
		close(done)
	}()
	t.Cleanup(func() {
		releaseHandler()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("accept loop did not stop")
		}
	})
	dial := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	first := dial()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("first connection was not accepted")
	}

	second := dial()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("second connection read error = %v, want EOF", err)
	}

	releaseHandler()
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := first.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("first connection read error = %v, want EOF after handler returns", err)
	}
	third := dial()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("connection was not accepted after the first session ended")
	}
	_ = third.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := third.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("third connection read error = %v, want EOF after handler returns", err)
	}
}

// seqTestHandler drives the real bridgeHandler.onFXPMessage path without a
// gateway connection.
type seqTestHandler struct {
	*bridgeHandler
	onLogon func()
}

func (h *seqTestHandler) OnLogon(s *session.Session) { h.sess = s; h.onLogon() }

func execReport(id string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
		ExecutionReport: &protobuf.ExecutionReport{
			OrderId: id, ExecutionId: "E-" + id, Symbol: "MSFT",
			ExecutionType: protobuf.ExecutionType_EXEC_NEW,
			OrderStatus:   protobuf.OrderStatus_ORDER_NEW,
			Side:          protobuf.OrderSide_BUY,
		},
	}}
}

// LogonAck carries 34=1, the first ExecutionReport 34=2, and every outbound
// message (including concurrently sent ones) is strictly monotonic and gap-free.
func TestOutboundSeqSingleCounter(t *testing.T) {
	const reports = 50
	srv, cli := net.Pipe()
	defer cli.Close()

	h := &seqTestHandler{bridgeHandler: &bridgeHandler{bridgeCompID: "FXPBRIDGE"}}
	h.onLogon = func() {
		h.onFXPMessage(execReport("ORD-0"))
		var wg sync.WaitGroup
		for i := 1; i < reports; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				h.onFXPMessage(execReport(fmt.Sprintf("ORD-%d", i)))
			}(i)
		}
		go wg.Wait()
	}
	sess := session.NewSession(srv, "FXPBRIDGE", h)
	sess.Auth = session.NewCredentialAuth(map[string]string{"TESTFIRM01": "pw"})
	h.sess = sess
	go sess.Run()

	go func() {
		logon := codec.NewBuilder(codec.MsgTypeLogon, "TESTFIRM01", "FXPBRIDGE", 1).
			SetInt(codec.TagEncryptMethod, 0).SetInt(codec.TagHeartBtInt, 30).Set(codec.TagPassword, "pw").Build()
		_, _ = cli.Write(logon)
	}()

	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))
	p := codec.NewParser(cli)
	for want := 1; want <= reports+1; want++ {
		msg, err := p.ReadMessage()
		if err != nil {
			t.Fatalf("reading outbound message %d: %v", want, err)
		}
		switch want {
		case 1:
			if msg.MsgType() != codec.MsgTypeLogon {
				t.Fatalf("first outbound message is %q, want LogonAck", msg.MsgType())
			}
		case 2:
			if msg.MsgType() != codec.MsgTypeExecutionReport {
				t.Fatalf("second outbound message is %q, want ExecutionReport", msg.MsgType())
			}
		}
		if got := msg.GetInt(codec.TagMsgSeqNum); got != want {
			t.Fatalf("outbound message %d (35=%s) has 34=%d", want, msg.MsgType(), got)
		}
	}
}

// Application messages that cannot reach the gateway are rejected, never
// silently dropped: before the gateway client exists and after it closed.
func TestOrdersWithoutGatewayAreRejected(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()
	h := &seqTestHandler{bridgeHandler: &bridgeHandler{bridgeCompID: "FXPBRIDGE"}, onLogon: func() {}}
	sess := session.NewSession(srv, "FXPBRIDGE", h)
	sess.Auth = session.NewCredentialAuth(map[string]string{"TESTFIRM01": "pw"})
	h.sess = sess
	go sess.Run()

	seq := 0
	send := func(msgType string, kv ...any) {
		seq++
		b := codec.NewBuilder(msgType, "TESTFIRM01", "FXPBRIDGE", seq)
		for i := 0; i+1 < len(kv); i += 2 {
			b.Set(kv[i].(int), kv[i+1].(string))
		}
		if _, err := cli.Write(b.Build()); err != nil {
			t.Errorf("write: %v", err)
		}
	}
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	p := codec.NewParser(cli)
	next := func() *codec.Message {
		m, err := p.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return m
	}
	order := []any{codec.TagClOrdID, "O1", codec.TagSymbol, "MSFT", codec.TagSide, "1", codec.TagOrderQty, "10",
		codec.TagOrdType, "2", codec.TagPrice, "10.00", codec.TagTransactTime, "20260101-00:00:00"}

	go send(codec.MsgTypeLogon, codec.TagEncryptMethod, "0", codec.TagHeartBtInt, "30", codec.TagPassword, "pw")
	next() // LogonAck

	go send(codec.MsgTypeNewOrderSingle, order...)
	if m := next(); m.MsgType() != codec.MsgTypeExecutionReport || m.Get(codec.TagExecType) != codec.ExecTypeRejected || m.Get(codec.TagClOrdID) != "O1" {
		t.Fatalf("order before gateway: 35=%s 150=%s 11=%s", m.MsgType(), m.Get(codec.TagExecType), m.Get(codec.TagClOrdID))
	}
	go send(codec.MsgTypeNewOrderSingle,
		codec.TagClOrdID, "O2", codec.TagSymbol, "MSFT", codec.TagOrderQty, "10",
		codec.TagOrdType, "2", codec.TagPrice, "10.00")
	if m := next(); m.MsgType() != codec.MsgTypeExecutionReport || m.Get(codec.TagExecType) != codec.ExecTypeRejected || m.Get(codec.TagText) == "" {
		t.Fatalf("missing Side reject: 35=%s 150=%s 58=%s", m.MsgType(), m.Get(codec.TagExecType), m.Get(codec.TagText))
	}
	go send(codec.MsgTypeNewOrderSingle,
		codec.TagClOrdID, "O3", codec.TagSymbol, "MSFT", codec.TagSide, "Z",
		codec.TagOrderQty, "10", codec.TagOrdType, "2", codec.TagPrice, "10.00")
	if m := next(); m.MsgType() != codec.MsgTypeExecutionReport || m.Get(codec.TagExecType) != codec.ExecTypeRejected || m.Get(codec.TagText) == "" {
		t.Fatalf("unknown Side reject: 35=%s 150=%s 58=%s", m.MsgType(), m.Get(codec.TagExecType), m.Get(codec.TagText))
	}
	go send(codec.MsgTypeOrderCancelRequest, codec.TagClOrdID, "C1", codec.TagOrigClOrdID, "O1", codec.TagSymbol, "MSFT", codec.TagSide, "1")
	if m := next(); m.MsgType() != codec.MsgTypeOrderCancelReject {
		t.Fatalf("cancel before gateway: 35=%s", m.MsgType())
	}
	go send("H", codec.TagClOrdID, "Q1")
	if m := next(); m.MsgType() != codec.MsgTypeBusinessMessageReject || m.Get(codec.TagRefMsgType) != "H" {
		t.Fatalf("unsupported type: 35=%s 372=%s", m.MsgType(), m.Get(codec.TagRefMsgType))
	}

	h.gwClient = gateway.NewClient("TESTFIRM01", "secret", func(*protobuf.FXPMessage) {})
	h.gwClient.Close()
	go send(codec.MsgTypeNewOrderSingle, order...)
	if m := next(); m.MsgType() != codec.MsgTypeExecutionReport || m.Get(codec.TagExecType) != codec.ExecTypeRejected {
		t.Fatalf("order after gateway closed: 35=%s 150=%s", m.MsgType(), m.Get(codec.TagExecType))
	}
}

// Core rejects of cancel/replace requests carry no Side, so they cannot be a
// valid FIX ExecutionReport; they must reach the client as OrderCancelReject.
func TestCancelRequestRejectsBecomeOrderCancelReject(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()
	loggedOn := make(chan struct{})
	h := &seqTestHandler{bridgeHandler: &bridgeHandler{bridgeCompID: "FXPBRIDGE"}, onLogon: func() { close(loggedOn) }}
	sess := session.NewSession(srv, "FXPBRIDGE", h)
	sess.Auth = session.NewCredentialAuth(map[string]string{"TESTFIRM01": "pw"})
	h.sess = sess
	go sess.Run()

	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	p := codec.NewParser(cli)
	next := func() *codec.Message {
		t.Helper()
		m, err := p.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return m
	}
	go func() {
		logon := codec.NewBuilder(codec.MsgTypeLogon, "TESTFIRM01", "FXPBRIDGE", 1).
			SetInt(codec.TagEncryptMethod, 0).SetInt(codec.TagHeartBtInt, 30).Set(codec.TagPassword, "pw").Build()
		_, _ = cli.Write(logon)
	}()
	next() // LogonAck
	<-loggedOn

	h.gwClient = gateway.NewClient("TESTFIRM01", "secret", func(*protobuf.FXPMessage) {})
	seq := 1
	request := func(msgType, clOrdID, origClOrdID string) {
		t.Helper()
		seq++
		raw := codec.NewBuilder(msgType, "TESTFIRM01", "FXPBRIDGE", seq).
			Set(codec.TagClOrdID, clOrdID).Set(codec.TagOrigClOrdID, origClOrdID).
			Set(codec.TagSymbol, "MSFT").Set(codec.TagSide, codec.SideBuy).Build()
		msg, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
		if err != nil {
			t.Fatalf("build %s: %v", msgType, err)
		}
		h.Handle(msg, sess)
	}
	coreReject := func(clientID, orderID string) *protobuf.FXPMessage {
		return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
			ExecutionReport: &protobuf.ExecutionReport{
				OrderId: orderID, ExecutionId: "REJ-" + clientID, Symbol: "MSFT",
				ExecutionType: protobuf.ExecutionType_EXEC_REJECTED,
				OrderStatus:   protobuf.OrderStatus_ORDER_REJECTED,
				ClientOrderId: clientID, OrigOrderId: orderID,
				RejectReason: "order not live",
			},
		}}
	}
	expectCancelReject := func(clOrdID, origClOrdID, responseTo string) {
		t.Helper()
		m := next()
		if m.MsgType() != codec.MsgTypeOrderCancelReject ||
			m.Get(codec.TagClOrdID) != clOrdID || m.Get(codec.TagOrigClOrdID) != origClOrdID ||
			m.Get(codec.TagOrderID) != origClOrdID || m.Get(codec.TagCxlRejResponseTo) != responseTo ||
			m.Get(codec.TagText) != "order not live" {
			t.Fatalf("got 35=%s 11=%s 41=%s 37=%s 434=%s 58=%q", m.MsgType(), m.Get(codec.TagClOrdID),
				m.Get(codec.TagOrigClOrdID), m.Get(codec.TagOrderID), m.Get(codec.TagCxlRejResponseTo), m.Get(codec.TagText))
		}
	}
	expectReport := func(orderID string) {
		t.Helper()
		if m := next(); m.MsgType() != codec.MsgTypeExecutionReport || m.Get(codec.TagOrderID) != orderID {
			t.Fatalf("got 35=%s 37=%s, want ExecutionReport for %s", m.MsgType(), m.Get(codec.TagOrderID), orderID)
		}
	}

	request(codec.MsgTypeOrderCancelRequest, "C1", "O1")
	h.onFXPMessage(coreReject("C1", "O1"))
	expectCancelReject("C1", "O1", "1")

	request(codec.MsgTypeOrderCancelReplaceRequest, "R1", "O2")
	h.onFXPMessage(coreReject("R1", "O2"))
	expectCancelReject("R1", "O2", "2")

	// Answered requests are forgotten; a side-less reject with no pending
	// request is still dropped rather than sent with a fabricated Side.
	h.onFXPMessage(coreReject("C1", "O1"))
	h.onFXPMessage(coreReject("UNKNOWN", "O9"))
	h.onFXPMessage(execReport("MARK-1"))
	expectReport("MARK-1")

	// A successful cancel also clears the pending request.
	request(codec.MsgTypeOrderCancelRequest, "C2", "O3")
	cancelled := execReport("O3")
	cancelled.GetExecutionReport().ExecutionType = protobuf.ExecutionType_EXEC_CANCELED
	cancelled.GetExecutionReport().OrderStatus = protobuf.OrderStatus_ORDER_CANCELED
	cancelled.GetExecutionReport().ClientOrderId = "C2"
	h.onFXPMessage(cancelled)
	expectReport("O3")
	h.onFXPMessage(coreReject("C2", "O3"))
	h.onFXPMessage(execReport("MARK-2"))
	expectReport("MARK-2")
}
