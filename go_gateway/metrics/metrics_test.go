// SPDX-License-Identifier: MIT
package metrics

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestAddressDefaultsAndInvalid(t *testing.T) {
	for _, value := range []string{"9091", ":9091", "localhost:9091"} {
		address, err := address(value)
		if err != nil || address != "127.0.0.1:9091" {
			t.Fatalf("%s: %s %v", value, address, err)
		}
	}
	for _, value := range []string{"bad", "host.example:90", "127.0.0.1:-1", "65536"} {
		if _, err := address(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if got, err := address("0.0.0.0:9091"); err != nil || got != "0.0.0.0:9091" {
		t.Fatal(got, err)
	}
}
func TestOptInAndFixedProtocolLabels(t *testing.T) {
	stop, err := Start(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if Enabled() {
		t.Fatal("metrics enabled by default")
	}
	enabled.Store(true)
	defer enabled.Store(false)
	before := clients.Load()
	done := Client()
	if clients.Load() != before+1 {
		t.Fatal("client not counted")
	}
	done()
	if clients.Load() != before {
		t.Fatal("client not released")
	}
	beforeIn, beforeOut, beforeReject := incoming[22].Load(), outgoing[8].Load(), rejects[6].Load()
	for _, m := range []*protobuf.FXPMessage{
		{Payload: &protobuf.FXPMessage_SessionControl{SessionControl: &protobuf.SessionControl{}}},
		{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderStatus: protobuf.OrderStatus_ORDER_REJECTED, RejectReason: "invalid private-order-123"}}},
	} {
		raw, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		Raw(raw, true)
		Raw(raw, false)
		Response(m)
	}
	if incoming[22].Load() != beforeIn+1 || outgoing[8].Load() != beforeOut+1 || rejects[6].Load() != beforeReject+1 {
		t.Fatal("incorrect counts")
	}
	text := render()
	if strings.Contains(text, "private-order") || !strings.Contains(text, "type=\"session_control\"") {
		t.Fatal("unbounded label or missing type")
	}
	for _, raw := range [][]byte{nil, {255}, {128, 255, 1}, {0}} {
		Raw(raw, true)
	}
}

func TestScrapeConnectionLimitRejectsSlowClientsAndRecovers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bounded := &scrapeListener{Listener: listener, slots: make(chan struct{}, maxScrapeConnections)}
	admitted := make(chan struct{}, maxScrapeConnections+1)
	var handled atomic.Int64
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handled.Add(1); _, _ = w.Write([]byte("healthy")) }),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				admitted <- struct{}{}
			}
		},
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(bounded) }()
	defer func() { _ = server.Close(); <-stopped }()
	var held []net.Conn
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	for range maxScrapeConnections {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		// An unfinished request must consume admission too, before any handler runs.
		_, err = io.WriteString(conn, "GET /metrics HTTP/1.1\r\nHost: localhost\r\n")
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-admitted:
		case <-time.After(time.Second):
			t.Fatal("connection not admitted")
		}
	}
	excess, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer excess.Close()
	_ = excess.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(excess, "GET /metrics HTTP/1.1\r\nHost: localhost\r\n\r\n")
	var byte [1]byte
	if n, err := excess.Read(byte[:]); n != 0 || err == nil {
		t.Fatal("over-capacity connection was served")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("excess connection queued rather than closed")
	}
	select {
	case <-admitted:
		t.Fatal("excess connection reached HTTP server")
	default:
	}
	if handled.Load() != 0 {
		t.Fatal("partial or excess request reached handler")
	}
	_ = held[0].Close()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := client.Get("http://" + listener.Addr().String() + "/metrics")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || string(body) != "healthy" {
				t.Fatal(string(body), readErr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection slot was not released", err)
		}
		time.Sleep(time.Millisecond)
	}
	if handled.Load() != 1 {
		t.Fatal("unexpected handler count", handled.Load())
	}
}

func TestScrapeConnectionCloseReleasesExactlyOnce(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	var released atomic.Int64
	conn := &scrapeConn{Conn: left, release: func() { released.Add(1) }}
	var closed sync.WaitGroup
	for range 10 {
		closed.Add(1)
		go func() { defer closed.Done(); _ = conn.Close() }()
	}
	closed.Wait()
	if released.Load() != 1 {
		t.Fatal("slot released more than once")
	}
}
