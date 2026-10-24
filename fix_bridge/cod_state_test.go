// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"fix_bridge/codec"
	"go_gateway/protobuf"
)

func codCancel(id string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{
		OrderId: id, ExecutionType: protobuf.ExecutionType_EXEC_CANCELED, RejectReason: "cancel on disconnect",
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
	if c.observe(codReport(0)) {
		t.Fatal("inactive codState consumed a message")
	}
	done := c.start()
	if c.observe(codCancel("A")) || isDone(done) {
		t.Fatal("ER must be forwarded and not complete COD")
	}
	if !c.observe(codReport(2)) {
		t.Fatal("COD report must not be forwarded to the FIX client")
	}
	if isDone(done) {
		t.Fatal("done before the second cancel")
	}
	c.observe(codCancel("B"))
	if !isDone(done) {
		t.Fatal("COD not complete after report and all cancels")
	}
	if c.observe(codReport(0)) {
		t.Fatal("finished codState consumed a later report")
	}
}

func TestCODEmptyBookCompletesOnReport(t *testing.T) {
	var c codState
	done := c.start()
	c.observe(codReport(0))
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
