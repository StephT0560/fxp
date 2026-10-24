package main

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"fix_bridge/codec"
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
	h.sess = sess
	go sess.Run()

	go func() {
		logon := codec.NewBuilder(codec.MsgTypeLogon, "TESTFIRM01", "FXPBRIDGE", 1).
			SetInt(codec.TagEncryptMethod, 0).SetInt(codec.TagHeartBtInt, 30).Build()
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
