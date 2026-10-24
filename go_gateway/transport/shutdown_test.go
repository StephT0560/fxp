// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func assertGoroutinesReturnNearBaseline(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine count did not return near baseline %d (got %d)", baseline, runtime.NumGoroutine())
}

func waitForRegisteredWriters(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		count := 0
		clientWriters.Range(func(_, _ any) bool {
			count++
			return true
		})
		if count >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("client writer did not register")
}

func TestTCPServerStopsAcceptingOnContextCancellation(t *testing.T) {
	t.Setenv("FXP_SHUTDOWN_TIMEOUT_SEC", "1")
	baseline := runtime.NumGoroutine()
	writerBaseline := 0
	clientWriters.Range(func(_, _ any) bool {
		writerBaseline++
		return true
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveTCPContext(ctx, listener)
		close(done)
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	waitForRegisteredWriters(t, writerBaseline+1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP server did not stop")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("TCP client did not receive EOF after cancellation")
	}
	_ = client.Close()
	if client, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond); err == nil {
		_ = client.Close()
		t.Fatal("TCP server accepted a new connection after cancellation")
	}
	assertGoroutinesReturnNearBaseline(t, baseline)
}

func TestWebSocketServerStopsAcceptingOnContextCancellation(t *testing.T) {
	t.Setenv("FXP_SHUTDOWN_TIMEOUT_SEC", "1")
	baseline := runtime.NumGoroutine()
	writerBaseline := 0
	clientWriters.Range(func(_, _ any) bool {
		writerBaseline++
		return true
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWebSocketServer(ctx, listener, time.Second) }()

	client, _, err := websocket.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRegisteredWriters(t, writerBaseline+1)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket server did not stop")
	}
	if _, _, err := client.ReadMessage(); err == nil {
		t.Fatal("WebSocket client did not receive EOF after cancellation")
	}
	_ = client.Close()
	if client, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond); err == nil {
		_ = client.Close()
		t.Fatal("WebSocket server accepted a new connection after cancellation")
	}
	assertGoroutinesReturnNearBaseline(t, baseline)
}
