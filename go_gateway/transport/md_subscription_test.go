// SPDX-License-Identifier: MIT

package transport

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
)

// Each core connection that sends a MarketDataRequest becomes a separate core
// subscriber, so subscriptions spread over the pool duplicated every update.
func TestSubscriptionsUseOneCoreConnection(t *testing.T) {
	oldPool, oldMD := corePool, mdConn
	t.Cleanup(func() { corePool, mdConn = oldPool, oldMD })
	corePool = make(chan *pooledConn, 3)
	var frames [3]atomic.Int32
	for i := range 3 {
		client, core := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = core.Close() })
		go func() {
			for {
				if _, err := readCoreFrame(core); err != nil {
					return
				}
				frames[i].Add(1)
			}
		}()
		pc := newPooledConnWith(i, client)
		if i == 1 {
			mdConn = pc
		}
		corePool <- pc
	}
	for range 6 {
		forwardSubscriptionToCore(&protobuf.MarketDataRequest{Symbols: []string{"AAPL"}})
		// Rotate the pool so a plain acquire() would pick another connection.
		corePool <- <-corePool
	}
	waitFor(t, "six subscriptions", func() bool { return frames[1].Load() == 6 })
	if frames[0].Load() != 0 || frames[2].Load() != 0 {
		t.Fatalf("subscriptions spread over pool: %d/%d/%d", frames[0].Load(), frames[1].Load(), frames[2].Load())
	}
}

func TestSubscriptionAggregateAndByteLimits(t *testing.T) {
	for i := range maxSubscriptionSymbols / maxSubscriptionsPerClient {
		client := new(int)
		t.Cleanup(func() { removeSubscriptions(client) })
		symbols := make([]string, maxSubscriptionsPerClient)
		for j := range symbols {
			symbols[j] = fmt.Sprintf("GLOBAL-%d-%d", i, j)
		}
		if _, ok := registerSubscriptions(client, symbols); !ok {
			t.Fatal("aggregate filled early")
		}
	}
	client := new(int)
	t.Cleanup(func() { removeSubscriptions(client) })
	if _, ok := registerSubscriptions(client, []string{"GLOBAL-0-0", "GLOBAL-OVERFLOW"}); ok {
		t.Fatal("aggregate budget exceeded")
	}
	if len(subscriptionsByClient[client]) != 0 {
		t.Fatal("aggregate rejection partially registered")
	}
	if _, ok := registerSubscriptions(client, []string{"GLOBAL-0-0"}); !ok {
		t.Fatal("shared symbol rejected at aggregate limit")
	}
	if _, ok := registerSubscriptions(client, []string{strings.Repeat("é", 65)}); ok {
		t.Fatal("symbol byte limit exceeded")
	}
}

func TestMarketDataAdmissionAndRejectionOnBothTransports(t *testing.T) {
	for _, transport := range []string{"tcp", "ws"} {
		t.Run(transport, func(t *testing.T) {
			oldMD := mdConn
			gateway, core := pipes(t)
			mdConn = newPooledConnWith(0, gateway)
			coreDone := make(chan struct{})
			go func() {
				defer close(coreDone)
				for {
					raw, err := readCoreFrame(core)
					if err != nil {
						return
					}
					msg := &protobuf.FXPMessage{}
					if proto.Unmarshal(raw, msg) != nil {
						return
					}
					req := msg.GetMarketDataRequest()
					if req != nil && req.ValidateOnly {
						reason := ""
						if slices.Contains(req.Symbols, "UNKNOWN") {
							reason = "unknown market-data symbol"
						}
						routeMarketDataValidation(&protobuf.MarketDataRequestAck{RequestId: req.RequestId, RejectReason: reason})
					}
				}
			}()
			defer func() { _ = gateway.Close(); <-coreDone; mdConn = oldMD }()
			done := make(chan struct{})
			var send func(*protobuf.FXPMessage)
			var receive func() *protobuf.FXPMessage
			if transport == "tcp" {
				server, client := pipes(t)
				go func() { defer close(done); handleTCPClient(server) }()
				send = func(msg *protobuf.FXPMessage) { writeMsg(t, client, msg) }
				receive = func() *protobuf.FXPMessage { return readMsg(t, client) }
				defer func() { _ = client.Close(); <-done }()
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); handleConnections(w, r) }))
				client, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
				if err != nil {
					server.Close()
					t.Fatal(err)
				}
				send = func(msg *protobuf.FXPMessage) {
					raw, _ := proto.Marshal(msg)
					if err := client.WriteMessage(websocket.BinaryMessage, raw); err != nil {
						t.Fatal(err)
					}
				}
				receive = func() *protobuf.FXPMessage {
					_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
					_, raw, err := client.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					msg := &protobuf.FXPMessage{}
					if err := proto.Unmarshal(raw, msg); err != nil {
						t.Fatal(err)
					}
					return msg
				}
				defer func() { _ = client.Close(); <-done; server.Close() }()
			}
			send(logonMsg(testJWT(t, "md-admission"), ""))
			if !receive().GetLogonAck().GetAccepted() {
				t.Fatal("logon rejected")
			}
			request := func(id string, symbols []string, unsubscribe bool) {
				send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{RequestId: id, Symbols: symbols, Unsubscribe: unsubscribe}}})
			}
			for _, tc := range []struct {
				id      string
				symbols []string
				kind    protobuf.ErrorType
			}{
				{"unknown", []string{"KNOWN", "UNKNOWN"}, protobuf.ErrorType_SYMBOL_NOT_SUPPORTED},
				{"long", []string{strings.Repeat("x", 129)}, protobuf.ErrorType_RATE_LIMIT_EXCEEDED},
			} {
				request(tc.id, tc.symbols, false)
				failure := receive().GetErrorMessage()
				if failure == nil || failure.RequestId != tc.id || failure.ErrorType != tc.kind {
					t.Fatalf("rejection = %v", failure)
				}
			}
			if _, ok := clientSubs.Load("UNKNOWN"); ok {
				t.Fatal("unknown symbol allocated shared quota")
			}
			if _, ok := clientSubs.Load("KNOWN"); ok {
				t.Fatal("mixed rejection partially admitted")
			}
			symbols := make([]string, maxSubscriptionsPerClient)
			for i := range symbols {
				symbols[i] = fmt.Sprintf("WIRE-%s-%d", transport, i)
			}
			request("full", symbols, false)
			if receive().GetMarketDataRequestAck().GetRequestId() != "full" {
				t.Fatal("missing admission ack")
			}
			request("overflow", []string{"WIRE-OVERFLOW"}, false)
			if receive().GetErrorMessage().GetErrorType() != protobuf.ErrorType_RATE_LIMIT_EXCEEDED {
				t.Fatal("connection budget not enforced on wire")
			}
			request("", symbols, true)
			request("reused", []string{"WIRE-OVERFLOW"}, false)
			if receive().GetMarketDataRequestAck().GetRequestId() != "reused" {
				t.Fatal("unsubscribe did not free quota")
			}
		})
	}
}

func readCoreFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
	body := make([]byte, n)
	_, err := io.ReadFull(r, body)
	return body, err
}

// When the core drops the market-data connection, the gateway must redial
// and replay subscriptions without waiting for a client request, and keep
// re-requesting a symbol until the core answers it with a snapshot.
func TestMarketDataConnectionRestoresSubscriptions(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
		}
	}()
	oldAddr, oldMD := coreAddr, mdConn
	coreAddr = ln.Addr().String()
	pc, err := newPooledConn(0)
	if err != nil {
		t.Fatal(err)
	}
	pc.md, pc.mdRetry = true, 20*time.Millisecond
	mdConn = pc
	subs := &sync.Map{}
	subs.Store("client", true)
	clientSubs.Store("RESTORE", subs)
	t.Cleanup(func() {
		pc.shutdown()
		_ = ln.Close()
		clientSubs.Delete("RESTORE")
		mdAwaiting.Delete("RESTORE")
		coreAddr, mdConn = oldAddr, oldMD
	})
	go poolReader(pc)

	accept := func() net.Conn {
		select {
		case c := <-conns:
			t.Cleanup(func() { _ = c.Close() })
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no core connection")
			return nil
		}
	}
	_ = accept().Close()
	second := accept()
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	for requests := 0; requests < 2; {
		body, err := readCoreFrame(second)
		if err != nil {
			t.Fatalf("after %d replayed subscriptions: %v", requests, err)
		}
		var msg protobuf.FXPMessage
		if proto.Unmarshal(body, &msg) == nil && msg.GetMarketDataRequest() != nil {
			if got := msg.GetMarketDataRequest().Symbols; !slices.Contains(got, "RESTORE") {
				t.Fatalf("replayed %v", got)
			}
			requests++
		}
	}
	mdAwaiting.Delete("RESTORE") // as when the snapshot arrives
	if n, err := replaySubscriptions(pc, true); n != 0 || err != nil {
		t.Fatalf("answered symbol re-requested: n=%d err=%v", n, err)
	}
}

// A departed client's subscriptions must not keep the restore loop retrying.
func TestUnregisterClientWriterDropsSubscriptions(t *testing.T) {
	client := new(int)
	if _, ok := registerSubscriptions(client, []string{"DEPARTED"}); !ok {
		t.Fatal("subscription rejected")
	}
	t.Cleanup(func() { removeSubscriptions(client) })
	mdAwaiting.Store("DEPARTED", struct{}{})
	t.Cleanup(func() { clientSubs.Delete("DEPARTED"); mdAwaiting.Delete("DEPARTED") })

	unregisterClientWriter(client)
	if _, ok := clientSubs.Load("DEPARTED"); ok {
		t.Fatal("subscription survived unregister")
	}
	if n, err := replaySubscriptions(&pooledConn{}, true); n != 0 || err != nil {
		t.Fatalf("departed symbol re-requested: n=%d err=%v", n, err)
	}
	if _, ok := mdAwaiting.Load("DEPARTED"); ok {
		t.Fatal("departed symbol still awaiting a snapshot")
	}
}

func TestSubscriptionBudgetIsAtomicAndReusable(t *testing.T) {
	client, other := new(int), new(int)
	t.Cleanup(func() {
		removeSubscriptions(client)
		removeSubscriptions(other)
	})
	symbols := make([]string, maxSubscriptionsPerClient)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("BUDGET-%d", i)
	}
	if got, ok := registerSubscriptions(client, append(slices.Clone(symbols), symbols[0])); !ok || len(got) != len(symbols) {
		t.Fatalf("duplicate counted against budget: %d, %t", len(got), ok)
	}
	if _, ok := registerSubscriptions(client, []string{symbols[0], "BUDGET-OVERFLOW"}); ok {
		t.Fatal("request exceeded the per-client budget")
	}
	if _, ok := clientSubs.Load("BUDGET-OVERFLOW"); ok {
		t.Fatal("rejected request left subscription state")
	}
	if _, ok := registerSubscriptions(other, symbols[:1]); !ok {
		t.Fatal("second client could not share a symbol")
	}
	removeSubscriptions(client)
	if _, ok := clientSubs.Load(symbols[0]); !ok {
		t.Fatal("removal dropped another client's subscription")
	}
	if _, ok := clientSubs.Load(symbols[1]); ok {
		t.Fatal("removal retained an unused symbol")
	}
	if _, ok := registerSubscriptions(client, symbols); !ok {
		t.Fatal("removed subscriptions still consume the budget")
	}
}
