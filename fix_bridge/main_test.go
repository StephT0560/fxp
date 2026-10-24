package main

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"fix_bridge/codec"
	"fix_bridge/gateway"
	"fix_bridge/session"
	"go_gateway/protobuf"
)

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
