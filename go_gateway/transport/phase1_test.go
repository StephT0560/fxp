//go:build phase1

// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"encoding/json"
	"go_gateway/protobuf"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Each update is acknowledged by every reader before the next injection. This
// excludes delivery loss, setup/warmup and producer-side fixture allocations.
func TestPhase1Attribution(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv("FXP_PHASE1_SUBSCRIBERS"))
	if err != nil {
		t.Skip("isolated fixture")
	}
	if n < 0 || n > 100 {
		t.Fatal("invalid subscribers")
	}
	symbol := "PHASE1"
	clients := new(sync.Map)
	clientSubs.Store(symbol, clients)
	defer clientSubs.Delete(symbol)
	ack := make(chan struct{}, 100)
	var peers []net.Conn
	var readers sync.WaitGroup
	var writers []*clientWriter
	for i := 0; i < n; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		peer, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		server, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = listener.Close()
		peers = append(peers, peer)
		clients.Store(server, true)
		writers = append(writers, registerClientWriter(server))
		readers.Add(1)
		go func() {
			defer readers.Done()
			var header [4]byte
			body := make([]byte, 65536)
			for {
				if _, err := io.ReadFull(peer, header[:]); err != nil {
					return
				}
				length := binary.BigEndian.Uint32(header[:])
				if length > uint32(len(body)) {
					return
				}
				if _, err := io.ReadFull(peer, body[:length]); err != nil {
					return
				}
				ack <- struct{}{}
			}
		}()
	}
	defer func() {
		for _, w := range writers {
			w.forceStop()
			<-w.finished
			clientWriters.Delete(w.conn)
		}
		for _, p := range peers {
			_ = p.Close()
		}
		readers.Wait()
	}()
	update := &protobuf.MarketDataIncrementalUpdate{Symbol: symbol, Changes: []*protobuf.MarketOrderUpdate{{Price: 100, Quantity: 1, Side: protobuf.OrderSide_BUY}}}
	watchdog := time.NewTimer(60 * time.Second)
	defer watchdog.Stop()
	inject := func() {
		ForwardMarketData(update)
		for i := 0; i < n; i++ {
			select {
			case <-ack:
			case <-watchdog.C:
				t.Fatal("missing update")
			}
		}
	}
	// Warm protobuf reflection and goroutine stacks; all measured frames same bytes.
	for i := 0; i < 128; i++ {
		inject()
	}
	phase1Encodes.Store(0)
	phase1Locks.Store(0)
	phase1Writes.Store(0)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 2000; i++ {
		inject()
	}
	runtime.ReadMemStats(&after)
	if phase1Encodes.Load() != 2000 || phase1Writes.Load() != uint64(2000*n) || phase1Locks.Load() != uint64(2000*n) {
		t.Fatalf("unexpected work %d/%d/%d", phase1Encodes.Load(), phase1Writes.Load(), phase1Locks.Load())
	}
	for _, w := range writers {
		if w.mdDropped.Load() != 0 {
			t.Fatal("dropped fixture output")
		}
	}
	result := map[string]uint64{"subscribers": uint64(n), "updates": 2000, "delivered": uint64(2000 * n), "encodes": phase1Encodes.Load(), "write_calls": phase1Writes.Load(), "enqueue_locks": phase1Locks.Load(), "allocations": after.Mallocs - before.Mallocs, "allocated_bytes": after.TotalAlloc - before.TotalAlloc}
	data, _ := json.Marshal(result)
	if err := os.WriteFile(os.Getenv("FXP_PHASE1_OUTPUT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPhase1SlowConsumer(t *testing.T) {
	oldLimits := currentLimits()
	limits := oldLimits
	limits.MDQueue = 16
	setLimits(limits)
	defer setLimits(oldLimits)
	if os.Getenv("FXP_PHASE1_OUTPUT") == "" {
		t.Skip("isolated fixture")
	}
	server, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	writer := registerClientWriter(server)
	defer func() { writer.forceStop(); <-writer.finished; clientWriters.Delete(server) }()
	raw, err := EncodeFXPMessage(&protobuf.MarketDataIncrementalUpdate{Symbol: "SLOW"})
	if err != nil {
		t.Fatal(err)
	}
	// First frame blocks in net.Pipe.Write because the peer never reads.
	if err = writer.enqueue(raw, true); err != nil {
		t.Fatal(err)
	}
	for len(writer.md) > 0 {
		runtime.Gosched()
	}
	for i := 0; i < 1000; i++ {
		if err = writer.enqueue(raw, true); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10000; i++ {
		if err = writer.enqueue(raw, true); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(writer.md) != cap(writer.md) || writer.mdDropped.Load() == 0 {
		t.Fatal("queue unbounded or no drops")
	}
	// Independent critical writer must deliver reports despite the blocked peer.
	fast, reader := net.Pipe()
	defer func() { _ = reader.Close() }()
	healthy := registerClientWriter(fast)
	defer func() { healthy.forceStop(); <-healthy.finished; clientWriters.Delete(fast) }()
	complete := make(chan struct{})
	var delivered int
	go func() {
		defer close(complete)
		var h [4]byte
		var b [256]byte
		for i := 0; i < 2000; i++ {
			if _, e := io.ReadFull(reader, h[:]); e != nil {
				return
			}
			if _, e := io.ReadFull(reader, b[:binary.BigEndian.Uint32(h[:])]); e != nil {
				return
			}
			delivered++
		}
	}()
	orderClientMap.Store("OWNER::ORDER", orderRoute{conn: fast, owner: "OWNER"})
	defer orderClientMap.Delete("OWNER::ORDER")
	for i := 0; i < 2000; i++ {
		routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderId: "OWNER::ORDER", OrderStatus: protobuf.OrderStatus_ORDER_NEW, ExecutionType: protobuf.ExecutionType_EXEC_NEW}}})
	}

	select {
	case <-complete:
	case <-time.After(10 * time.Second):
		t.Fatal("order delivery blocked")
	}
	if delivered != 2000 {
		t.Fatalf("delivered %d reports", delivered)
	}
	if after.HeapAlloc > before.HeapAlloc+(1<<20) {
		t.Fatal("unexpected retained heap growth")
	}
	data, _ := json.Marshal(map[string]any{"queue_depth": len(writer.md), "capacity": cap(writer.md), "drops": writer.mdDropped.Load(), "retained_payload_bytes": len(writer.md) * len(raw), "heap_after_saturation": before.HeapAlloc, "heap_after_10000_drops_and_gc": after.HeapAlloc, "order_reports": 2000})
	if err = os.WriteFile(os.Getenv("FXP_PHASE1_OUTPUT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
