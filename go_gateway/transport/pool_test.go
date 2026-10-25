// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
)

func coreFrame(t *testing.T, m *protobuf.FXPMessage) []byte {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	return frame
}

func poolReaderGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "transport.poolReader(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A fake core drops the pooled connection drops times. Every connection
// generation streams frames tagged "gen:seq"; the gateway must read each
// generation in order with exactly one reader and no leaked goroutines.
func TestPoolReconnectKeepsSingleReader(t *testing.T) {
	const drops, perConn = 8, 200

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		seen  = map[int][]int{}
		bad   []string
		conns []net.Conn
	)
	oldAddr, oldPool, oldHandler := coreAddr, corePool, onCoreMessage
	coreAddr = ln.Addr().String()
	onCoreMessage = func(m *protobuf.FXPMessage) {
		var g, i int
		hb := m.GetHeartbeat()
		if hb == nil {
			mu.Lock()
			bad = append(bad, fmt.Sprintf("%T", m.Payload))
			mu.Unlock()
			return
		}
		if _, err := fmt.Sscanf(hb.Status, "%d:%d", &g, &i); err != nil {
			mu.Lock()
			bad = append(bad, hb.Status)
			mu.Unlock()
			return
		}
		mu.Lock()
		seen[g] = append(seen[g], i)
		mu.Unlock()
	}

	go func() {
		for g := 0; ; g++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() { _, _ = io.Copy(io.Discard, c) }()
			var out bytes.Buffer
			for i := 0; i < perConn; i++ {
				out.Write(coreFrame(t, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Heartbeat{
					Heartbeat: &protobuf.Heartbeat{Status: fmt.Sprintf("%d:%d", g, i)},
				}}))
			}
			// Dribble the bytes so a second reader would interleave mid-frame.
			for b := out.Bytes(); len(b) > 0; {
				n := min(7, len(b))
				if _, err := c.Write(b[:n]); err != nil {
					break
				}
				b = b[n:]
			}
			if g < drops {
				// Deliver every generation's frames before EOF. Closing with
				// unread heartbeat writes can send RST and discard outbound
				// bytes, making the exact-frame assertions nondeterministic.
				// A half-close still forces the pool reader to reconnect.
				if err := c.(*net.TCPConn).CloseWrite(); err != nil {
					t.Error(err)
				}
			}
		}
	}()

	pc, err := newPooledConn(0)
	if err != nil {
		t.Fatal(err)
	}
	corePool = make(chan *pooledConn, 1)
	corePool <- pc
	go poolReader(pc)
	t.Cleanup(func() {
		_ = ln.Close()
		pc.shutdown()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		coreAddr, corePool, onCoreMessage = oldAddr, oldPool, oldHandler
	})

	got := func(g int) int { mu.Lock(); defer mu.Unlock(); return len(seen[g]) }
	waitFor(t, "generation 0", func() bool { return got(0) == perConn })
	baseline := runtime.NumGoroutine()

	frame := coreFrame(t, heartbeat(0))
	for g := 1; g <= drops; g++ {
		// The core has closed generation g-1; sends reconnect until g arrives.
		waitFor(t, fmt.Sprintf("generation %d", g), func() bool {
			if got(g) == 0 {
				SendFramedToCore(nil, frame, "T::churn", "T", false)
			}
			return got(g) == perConn
		})
		if n := poolReaderGoroutines(); n != 1 {
			t.Fatalf("after reconnect %d: %d poolReader goroutines, want 1", g, n)
		}
	}
	orderClientMap.Delete("T::churn")

	mu.Lock()
	for g := 0; g <= drops; g++ {
		for i, v := range seen[g] {
			if v != i {
				t.Errorf("generation %d: frame %d arrived at position %d", g, v, i)
				break
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("corrupted frames: %v", bad)
	}
	mu.Unlock()
	waitFor(t, "stable goroutine count", func() bool { return runtime.NumGoroutine() <= baseline+1 })
}

// Writing on a pooled connection whose reader marked it dead must fail
// cleanly (so the caller reconnects) instead of dereferencing a nil conn.
func TestPooledConnWriteAfterDropReturnsError(t *testing.T) {
	pc := &pooledConn{}
	if err := pc.write([]byte{0, 0, 0, 0}); err == nil {
		t.Fatal("write on a dead pooled conn succeeded")
	}
}

// A bad length prefix means the stream is no longer frame-aligned: the reader
// must give up on the connection rather than keep reading garbage.
func TestReadCoreFramesDropsMisalignedStream(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	go func() {
		_, _ = b.Write([]byte{0, 0, 0, 0})
		_, _ = io.Copy(io.Discard, b)
	}()
	done := make(chan error, 1)
	go func() { done <- readCoreFrames(a) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "invalid frame length") {
			t.Fatalf("got %v, want invalid frame length", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader kept reading a misaligned stream")
	}
	_ = b.Close()
}
