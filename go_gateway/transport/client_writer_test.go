// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestRouteFromCoreDoesNotBlockOnStalledClient(t *testing.T) {
	server, peer, done := startTCPWriterHarness(t)
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("TCP handler did not return")
		}
	})

	const orderID = "T::SLOW"
	orderClientMap.Store(orderID, orderRoute{conn: server, owner: "T"})
	t.Cleanup(func() { orderClientMap.Delete(orderID) })

	routed := make(chan struct{})
	go func() {
		routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
			ExecutionReport: &protobuf.ExecutionReport{
				OrderId: orderID, OrderStatus: protobuf.OrderStatus_ORDER_NEW,
			},
		}})
		close(routed)
	}()
	select {
	case <-routed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("routeFromCore blocked on a stalled client")
	}
}

func TestUnregisteredTCPOutputIsDropped(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = peer.Close() }()
	_ = server.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	err := enqueueClientData(server, []byte("critical output"), false)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("enqueueClientData error = %v, want %v", err, net.ErrClosed)
	}
	if n, err := peer.Read(make([]byte, 64)); n != 0 || !isTimeout(err) {
		t.Fatalf("unregistered TCP client received output: n=%d err=%v", n, err)
	}
}

func TestUnregisteredWebSocketOutputIsDropped(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConnCh <- conn
		}
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not upgrade")
	}
	defer func() { _ = serverConn.Close() }()

	err = enqueueClientData(serverConn, []byte("market data"), true)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("enqueueClientData error = %v, want %v", err, net.ErrClosed)
	}
	_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := client.ReadMessage(); !isTimeout(err) {
		t.Fatalf("unregistered WebSocket client received output: %v", err)
	}
}

func TestCriticalQueueOverflowClosesStalledClient(t *testing.T) {
	server, peer, done := startTCPWriterHarness(t)
	orderIDs := make([]string, 8_195)
	for i := range orderIDs {
		orderIDs[i] = "T::overflow-" + strconv.Itoa(i)
		orderClientMap.Store(orderIDs[i], orderRoute{conn: server, owner: "T"})
	}
	t.Cleanup(func() {
		for _, id := range orderIDs {
			orderClientMap.Delete(id)
		}
	})

	routed := make(chan struct{})
	go func() {
		for _, id := range orderIDs {
			routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{
				ExecutionReport: &protobuf.ExecutionReport{
					OrderId: id, OrderStatus: protobuf.OrderStatus_ORDER_NEW,
				},
			}})
		}
		close(routed)
	}()
	select {
	case <-routed:
	case <-time.After(2 * time.Second):
		t.Fatal("critical queue overflow blocked the core router")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	_, err := io.Copy(io.Discard, peer)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("client was not closed after critical queue overflow: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP handler did not return after critical queue overflow")
	}
}

func TestMarketDataFanoutDropsSlowClientWithinBoundedQueue(t *testing.T) {
	withLimits(t, func(l *Limits) {
		l.ClientQueue = 8
		l.MDQueue = 16
	})
	before := runtime.NumGoroutine()
	droppedBefore := MarketDataDropped()
	slow, slowPeer, slowDone := startTCPWriterHarness(t)
	healthy, healthyPeer, healthyDone := startTCPWriterHarness(t)
	subscribers := &sync.Map{}
	subscribers.Store(slow, true)
	subscribers.Store(healthy, true)
	clientSubs.Store("WRITER-MD", subscribers)
	t.Cleanup(func() {
		clientSubs.Delete("WRITER-MD")
		_ = slowPeer.Close()
		_ = healthyPeer.Close()
	})

	const orderedCount = 1_000
	sequences := make(chan uint64, orderedCount)
	readDone := make(chan error, 1)
	go func() {
		for i := 0; i < orderedCount; i++ {
			body, err := readFrame(healthyPeer, 1<<20)
			if err != nil {
				readDone <- err
				return
			}
			msg := &protobuf.FXPMessage{}
			if err := proto.Unmarshal(body, msg); err != nil {
				readDone <- err
				return
			}
			sequences <- msg.GetMarketDataIncremental().GetHeader().GetSequenceNumber()
		}
		readDone <- nil
	}()

	for i := 0; i < orderedCount; i++ {
		ForwardMarketData(&protobuf.MarketDataIncrementalUpdate{
			Symbol: "WRITER-MD",
			Header: &protobuf.FXPHeader{SequenceNumber: uint64(i + 1)},
		})
		time.Sleep(50 * time.Microsecond)
	}
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("healthy client read failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("healthy client did not receive ordered market data")
	}
	for i := 0; i < orderedCount; i++ {
		if got := <-sequences; got != uint64(i+1) {
			t.Fatalf("healthy market data out of order: got %d at %d", got, i)
		}
	}

	for i := 0; i < 100_000; i++ {
		ForwardMarketData(&protobuf.MarketDataIncrementalUpdate{
			Symbol: "WRITER-MD",
			Header: &protobuf.FXPHeader{SequenceNumber: uint64(orderedCount + i + 1)},
		})
	}
	value, ok := clientWriters.Load(slow)
	if !ok {
		t.Fatal("slow client writer unexpectedly unregistered")
	}
	writer := value.(*clientWriter)
	if cap(writer.md) != 16 || len(writer.md) > cap(writer.md) {
		t.Fatalf("market-data queue exceeded its bound: len=%d cap=%d", len(writer.md), cap(writer.md))
	}
	if MarketDataDropped() <= droppedBefore {
		t.Fatal("market-data overflow was not counted")
	}
	slowBytes, err := proto.Marshal(&protobuf.MarketDataIncrementalUpdate{Symbol: "WRITER-MD"})
	if err != nil {
		t.Fatal(err)
	}
	if queued := cap(writer.md) * (len(slowBytes) + 4); queued > 16*(len(slowBytes)+4) {
		t.Fatalf("market-data queue memory exceeded bound: %d bytes", queued)
	}

	_ = slowPeer.Close()
	_ = healthyPeer.Close()
	select {
	case <-slowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("slow TCP handler did not return")
	}
	select {
	case <-healthyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy TCP handler did not return")
	}
	select {
	case <-writer.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("client writer goroutine did not return")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+3 {
		t.Fatalf("goroutines did not return near baseline: before=%d after=%d", before, got)
	}
}

func TestConcurrentWebSocketOutputsUseOneWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(handleConnections))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var serverConn *websocket.Conn
	waitUntil(t, func() bool {
		clientWriters.Range(func(key, _ any) bool {
			if conn, ok := key.(*websocket.Conn); ok {
				serverConn = conn
				return false
			}
			return true
		})
		return serverConn != nil
	})
	subscribers := &sync.Map{}
	subscribers.Store(serverConn, true)
	clientSubs.Store("WRITER-WS", subscribers)
	t.Cleanup(func() { clientSubs.Delete("WRITER-WS") })

	const workers, iterations = 16, 10
	panics := make(chan any, workers)
	errorsSeen := make(chan error, workers*iterations)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					panics <- recovered
				}
			}()
			for i := 0; i < iterations; i++ {
				err := writeFXPMessage(serverConn, &protobuf.FXPMessage{
					Payload: &protobuf.FXPMessage_ErrorMessage{
						ErrorMessage: &protobuf.ErrorMessage{ErrorMessage: "concurrent"},
					},
				})
				if err != nil {
					errorsSeen <- err
				}
				ForwardMarketData(&protobuf.MarketDataIncrementalUpdate{Symbol: "WRITER-WS"})
				sendSessionError(serverConn, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "concurrent")
			}
		}()
	}
	wg.Wait()
	select {
	case recovered := <-panics:
		t.Fatalf("concurrent WebSocket write panicked: %v", recovered)
	default:
	}
	select {
	case err := <-errorsSeen:
		t.Fatalf("WebSocket output enqueue failed: %v", err)
	default:
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < workers*iterations*3; i++ {
		if _, _, err := client.ReadMessage(); err != nil {
			t.Fatalf("reading concurrent output %d: %v", i, err)
		}
	}
}

func startTCPWriterHarness(t *testing.T) (net.Conn, net.Conn, <-chan struct{}) {
	t.Helper()
	server, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleTCPClient(server)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	return server, peer, done
}

func writeFramed(t *testing.T, conn net.Conn, body []byte) {
	t.Helper()
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}
