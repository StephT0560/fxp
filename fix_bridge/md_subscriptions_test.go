// SPDX-License-Identifier: MIT

package main

import (
	"slices"
	"testing"

	"go_gateway/protobuf"
)

func wantIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func TestMDSubscriptionsOverlappingRequests(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL"})
	s.subscribe("R2", true, []string{"AAPL"})
	wantIDs(t, "snapshot", s.snapshotTargets("AAPL"), "R1", "R2")
	wantIDs(t, "incremental", s.incrementalTargets("AAPL"), "R1", "R2")
	s.unsubscribe("R2", nil)
	wantIDs(t, "incremental after R2 unsubscribe", s.incrementalTargets("AAPL"), "R1")
	wantIDs(t, "other symbol", s.incrementalTargets("MSFT"))
}

func TestMDAdmissionRejectPreservesLiveRouting(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL"})
	s.snapshotTargets("AAPL")
	key, err := s.prepare("R1", false, []string{"AAPL", "BAD"})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, "existing request before admission", s.incrementalTargets("AAPL"), "R1")
	if req, ok := s.reject(key); !ok || req.id != "R1" {
		t.Fatal("rejection lost correlation")
	}
	wantIDs(t, "existing request after rejection", s.incrementalTargets("AAPL"), "R1")
	wantIDs(t, "rejected routing", s.snapshotTargets("BAD"))
}

func TestMDUnsubscribeBeforeAdmissionAndSnapshotRelease(t *testing.T) {
	var s mdSubscriptions
	key, err := s.prepare("R1", false, []string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatal(err)
	}
	s.unsubscribe("R1", []string{"AAPL"})
	s.admit(key)
	wantIDs(t, "unsubscribed pending request", s.snapshotTargets("AAPL"))
	wantIDs(t, "admitted snapshot", s.snapshotTargets("MSFT"), "R1")
	s.mu.Lock()
	defer s.mu.Unlock()
	wantIDs(t, "completed symbols released", s.unusedLocked([]string{"AAPL", "MSFT"}), "AAPL", "MSFT")
}

func TestMDPendingRequestPreventsPrematureRelease(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("SNAP", false, []string{"AAPL"})
	key, err := s.prepare("STREAM", true, []string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	s.snapshotTargets("AAPL")
	s.mu.Lock()
	wantIDs(t, "pending request retains symbol", s.unusedLocked([]string{"AAPL"}))
	s.mu.Unlock()
	s.admit(key)
	wantIDs(t, "new stream snapshot", s.snapshotTargets("AAPL"), "STREAM")
}

func TestGatewayMDRejectionReachesFIXClient(t *testing.T) {
	h, _, _, next := edgeSession(t)
	key, err := h.md.prepare("CLIENT-MD-ID", true, []string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	h.onFXPMessage(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ErrorMessage{ErrorMessage: &protobuf.ErrorMessage{
		RequestId: key, ErrorType: protobuf.ErrorType_RATE_LIMIT_EXCEEDED, ErrorMessage: "subscription limit exceeded",
	}}})
	reply := next()
	if reply.MsgType() != "Y" || reply.Get(262) != "CLIENT-MD-ID" || reply.Get(281) != "2" || reply.Get(58) != "subscription limit exceeded" {
		t.Fatalf("FIX reject = %v", reply)
	}
	wantIDs(t, "rejected request routing", h.md.snapshotTargets("AAPL"))
}

func TestMDSubscriptionsSnapshotOnlyGetsOneSnapshot(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", false, []string{"AAPL"})
	wantIDs(t, "incremental before snapshot", s.incrementalTargets("AAPL"))
	wantIDs(t, "snapshot", s.snapshotTargets("AAPL"), "R1")
	wantIDs(t, "second snapshot", s.snapshotTargets("AAPL"))
	wantIDs(t, "incremental after snapshot", s.incrementalTargets("AAPL"))
}

func TestMDSubscriptionsStreamStartsAfterOwnSnapshot(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL"})
	wantIDs(t, "incremental before snapshot", s.incrementalTargets("AAPL"))
	wantIDs(t, "snapshot", s.snapshotTargets("AAPL"), "R1")
	// A snapshot triggered by another subscriber is not re-delivered.
	wantIDs(t, "rebroadcast snapshot", s.snapshotTargets("AAPL"))
	wantIDs(t, "incremental", s.incrementalTargets("AAPL"), "R1")
}

func TestMDSubscriptionsResubscribeSameIDReplaces(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL"})
	s.snapshotTargets("AAPL")
	s.subscribe("R1", true, []string{"AAPL"})
	wantIDs(t, "snapshot", s.snapshotTargets("AAPL"), "R1")
	wantIDs(t, "incremental", s.incrementalTargets("AAPL"), "R1")
}

func TestMDSubscriptionsUndoRestoresPreviousState(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL"})
	s.snapshotTargets("AAPL")
	undo := s.subscribe("R1", false, []string{"AAPL", "MSFT"})
	undo()
	wantIDs(t, "AAPL incremental", s.incrementalTargets("AAPL"), "R1")
	wantIDs(t, "MSFT snapshot", s.snapshotTargets("MSFT"))
}

func TestMDSubscriptionsPartialUnsubscribe(t *testing.T) {
	var s mdSubscriptions
	s.subscribe("R1", true, []string{"AAPL", "MSFT"})
	s.snapshotTargets("AAPL")
	s.snapshotTargets("MSFT")
	s.unsubscribe("R1", []string{"AAPL"})
	wantIDs(t, "AAPL after unsubscribe", s.incrementalTargets("AAPL"))
	wantIDs(t, "MSFT still streaming", s.incrementalTargets("MSFT"), "R1")
	s.unsubscribe("R1", nil)
	wantIDs(t, "MSFT after unsubscribing every symbol", s.incrementalTargets("MSFT"))
}

func TestTradeCaptureReportRoutesOnlyToStreamingSymbolSubscribers(t *testing.T) {
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_TradeCaptureReport{
		TradeCaptureReport: &protobuf.TradeCaptureReport{Symbol: "MDSYM"},
	}}
	symbol, snapshot, ok := marketDataSymbol(msg)
	if !ok || snapshot || symbol != "MDSYM" {
		t.Fatalf("marketDataSymbol = (%q, %v, %v), want (MDSYM, false, true)", symbol, snapshot, ok)
	}
	var subscriptions mdSubscriptions
	subscriptions.subscribe("STREAM", true, []string{"MDSYM"})
	subscriptions.subscribe("OTHER", true, []string{"OTHER"})
	subscriptions.snapshotTargets("MDSYM")
	subscriptions.snapshotTargets("OTHER")
	wantIDs(t, "trade-feed subscribers", subscriptions.incrementalTargets(symbol), "STREAM")
}
