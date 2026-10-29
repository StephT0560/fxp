// SPDX-License-Identifier: MIT
package transport

import (
	"errors"
	"net"
	"testing"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestSubscriberDropLeavesDetectableTradeGap(t *testing.T) {
	withLimits(t, func(l *Limits) { l.MDQueue = 1 })
	server, peer := net.Pipe()
	writer := registerClientWriter(server)
	defer func() { writer.forceStop(); <-writer.finished; clientWriters.Delete(server); _ = peer.Close() }()
	send := func(seq uint64) {
		raw, err := proto.Marshal(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_TradeCaptureReport{TradeCaptureReport: &protobuf.TradeCaptureReport{Symbol: "GAP", Seq: seq}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.enqueue(raw, true); err != nil {
			t.Fatal(err)
		}
	}
	send(1)
	// No reader: first frame must leave the queue and block in the pipe writer.
	waitUntil(t, func() bool { return len(writer.md) == 0 })
	send(2)
	send(3)
	if writer.mdDropped.Load() != 1 || len(writer.md) != 1 {
		t.Fatal("drop-newest policy or bound changed")
	}
	for _, want := range []uint64{1, 2} {
		if got := readCODMsg(t, peer).GetTradeCaptureReport().GetSeq(); got != want {
			t.Fatalf("seq=%d want=%d", got, want)
		}
	}
	send(4)
	if got := readCODMsg(t, peer).GetTradeCaptureReport().GetSeq(); got != 4 {
		t.Fatalf("missing detectable 2->4 gap: %d", got)
	}
}

func TestCriticalOverflowTriggersCancelOnDisconnect(t *testing.T) {
	withLimits(t, func(l *Limits) { l.ClientQueue = 2; l.MDQueue = 2 })
	frames := fakeCore(t)
	peer := gatewaySession(t, "COD-OVERFLOW", true)
	var writer *clientWriter
	clientWriters.Range(func(_, value any) bool {
		candidate := value.(*clientWriter)
		// The only session in this serial fixture is authenticated and idle.
		writer = candidate
		return false
	})
	if writer == nil {
		t.Fatal("session writer missing")
	}
	var overflow bool
	for i := 0; i < 10; i++ {
		err := writer.enqueue([]byte("report"), false)
		if errors.Is(err, errClientQueueFull) {
			overflow = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !overflow {
		t.Fatal("critical output was silently lost")
	}
	expectDisconnectCancel(t, frames, "COD-OVERFLOW")
	select {
	case <-writer.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("writer leaked")
	}
	_ = peer.Close()
}
