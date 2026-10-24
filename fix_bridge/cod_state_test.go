// SPDX-License-Identifier: MIT

package main

import (
	"net"
	"testing"
	"time"

	"fix_bridge/codec"
	"fix_bridge/session"
	"go_gateway/protobuf"
)

func codCancel(id string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{
		OrderId: id, Symbol: "AAPL", Side: protobuf.OrderSide_BUY, OrderStatus: protobuf.OrderStatus_ORDER_CANCELED,
		ExecutionType: protobuf.ExecutionType_EXEC_CANCELED, RejectReason: "cancel on disconnect",
	}}}
}

func codReport(total uint32) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{
		OrderMassCancelReport: &protobuf.OrderMassCancelReport{ClientRequestId: codRequestID, TotalAffected: total},
	}}
}

func isDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// The report and the canceled ERs travel on different core connections, so
// COD completes only once both the report and every ER it counts arrived.
func TestCODWaitsForReportAndAllCancels(t *testing.T) {
	var c codState
	if c.observeReport(codReport(0)) {
		t.Fatal("inactive codState consumed a message")
	}
	done := c.start()
	if c.countCancel(codCancel("A")); isDone(done) {
		t.Fatal("ER must be forwarded and not complete COD")
	}
	if !c.observeReport(codReport(2)) {
		t.Fatal("COD report must not be forwarded to the FIX client")
	}
	if isDone(done) {
		t.Fatal("done before the second cancel")
	}
	c.countCancel(codCancel("B"))
	if !isDone(done) {
		t.Fatal("COD not complete after report and all cancels")
	}
	if c.observeReport(codReport(0)) {
		t.Fatal("finished codState consumed a later report")
	}
}

func TestCODEmptyBookCompletesOnReport(t *testing.T) {
	var c codState
	done := c.start()
	c.observeReport(codReport(0))
	if !isDone(done) {
		t.Fatal("COD with nothing to cancel did not complete")
	}
}

func TestFixRequestForMassCancel(t *testing.T) {
	req := fixRequestFor(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancel{
		OrderMassCancel: &protobuf.OrderMassCancelRequest{ClientRequestId: "MC9"},
	}})
	if req == nil || req.MsgType() != codec.MsgTypeOrderMassCancelRequest || req.Get(codec.TagClOrdID) != "MC9" {
		t.Fatalf("got %v", req)
	}
}

type gateStore struct {
	session.MessageStore
	entered chan struct{}
	release chan struct{}
}

func (g *gateStore) Save(seq int, frame []byte) error {
	g.entered <- struct{}{}
	<-g.release
	return g.MessageStore.Save(seq, frame)
}

// COD completes only after its last cancel is stored; otherwise the session
// could release its store with that cancel missing from resend history.
func TestCODCompletesOnlyAfterLastCancelIsStored(t *testing.T) {
	srv, cli := net.Pipe()
	defer func() { _ = cli.Close(); _ = srv.Close() }()
	h := &bridgeHandler{bridgeCompID: "FXPBRIDGE"}
	h.sess = session.NewSession(srv, "FXPBRIDGE", h)
	gs := &gateStore{MessageStore: h.sess.Store, entered: make(chan struct{}), release: make(chan struct{})}
	h.sess.Store = gs
	done := h.cod.start()
	h.onFXPMessage(codReport(1))
	go h.onFXPMessage(codCancel("A"))
	select {
	case <-gs.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel ER was never stored")
	}
	stored := isDone(done)
	close(gs.release)
	if stored {
		t.Fatal("COD completed before its last cancel was stored")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("COD did not complete after the cancel was stored")
	}
}
