//go:build phase1

// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/json"
	"net"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

// Same process/session, same routed ACK and update work in both halves. A
// net.Pipe makes the stalled writer deterministic, independent of kernel buffers.
// Four is predeclared in phase1-baseline.md; timings are never serialized.
func TestSubscriberIsolationRelative(t *testing.T) {
	withLimits(t, func(l *Limits) { l.ClientQueue = 16; l.MDQueue = 16 })
	baselineG := runtime.NumGoroutine()
	healthy, peer := net.Pipe()
	fast := registerClientWriter(healthy)
	defer func() { fast.forceStop(); <-fast.finished; clientWriters.Delete(healthy); _ = peer.Close() }()
	orderClientMap.Store("ISOLATION::ORDER", orderRoute{conn: healthy, owner: "ISOLATION"})
	defer orderClientMap.Delete("ISOLATION::ORDER")
	clients := new(sync.Map)
	clientSubs.Store("ISOLATION", clients)
	defer clientSubs.Delete("ISOLATION")
	update := &protobuf.MarketDataIncrementalUpdate{Symbol: "ISOLATION"}
	report := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderId: "ISOLATION::ORDER", OrderStatus: protobuf.OrderStatus_ORDER_NEW}}}
	_ = peer.SetReadDeadline(time.Now().Add(60 * time.Second))
	measure := func() time.Duration {
		start := time.Now()
		for i := 0; i < 2000; i++ {
			ForwardMarketData(update)
			routeFromCore(proto.Clone(report).(*protobuf.FXPMessage))
			body, err := readFrame(peer, 65536)
			if err != nil || len(body) == 0 {
				t.Fatalf("healthy ACK missing: %v", err)
			}
		}
		return time.Since(start)
	}
	// Warm reflection and stacks before pairing.
	_ = measure()
	ratios := make([]float64, 0, 5)
	depths := make([]int, 0, 5)
	drops := make([]uint64, 0, 5)
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memoryBefore)
	for pair := 0; pair < 5; pair++ {
		control := measure()
		slow, stalled := net.Pipe()
		writer := registerClientWriter(slow)
		clients.Store(slow, true)
		// Saturate before timing; one in-flight plus exactly 16 queued frames.
		for i := 0; i < 1000; i++ {
			ForwardMarketData(update)
		}
		elapsed := measure()
		ratios = append(ratios, float64(elapsed)/float64(control))
		if len(writer.md) != 16 || writer.mdDropped.Load() < 2983 {
			t.Fatal("queue not bounded, writer not stalled, or drops uncounted")
		}
		depths = append(depths, len(writer.md))
		drops = append(drops, writer.mdDropped.Load())
		// Ten thousand more drops cannot retain another ten thousand payloads.
		for i := 0; i < 10000; i++ {
			ForwardMarketData(update)
		}
		clients.Delete(slow)
		writer.forceStop()
		select {
		case <-writer.finished:
		case <-time.After(time.Second):
			t.Fatal("stalled writer leaked")
		}
		clientWriters.Delete(slow)
		_ = stalled.Close()
	}
	runtime.GC()
	runtime.ReadMemStats(&memoryAfter)
	if memoryAfter.HeapAlloc > memoryBefore.HeapAlloc+(2<<20) {
		t.Fatal("retained heap grew beyond 2 MiB fixture allowance")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baselineG+3 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if runtime.NumGoroutine() > baselineG+3 {
		t.Fatal("stalled subscriber goroutines leaked")
	}
	sorted := slices.Clone(ratios)
	slices.Sort(sorted)
	if sorted[2] > 4 {
		t.Fatalf("median stalled/control slowdown %.3fx exceeds predeclared 4x: %v", sorted[2], ratios)
	}
	result := map[string]any{"label": "relative, shared hardware; functional same-session check", "stalled_over_control_elapsed_ratios": ratios, "median": sorted[2], "min": sorted[0], "max": sorted[4], "median_ceiling": 4, "reports_per_half": 2000, "pairs": 5, "queue_depths": depths, "drops_before_extra_10000": drops, "heap_growth_bytes": int64(memoryAfter.HeapAlloc) - int64(memoryBefore.HeapAlloc), "heap_allowance_bytes": 2 << 20, "goroutines_before": baselineG, "goroutines_after": runtime.NumGoroutine(), "queued_payload_bound": "16 frames plus one in-flight frame"}
	if path := os.Getenv("FXP_ISOLATION_OUTPUT"); path != "" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("stalled/control ratios %v, median %.3fx (ceiling 4x)", ratios, sorted[2])
}
