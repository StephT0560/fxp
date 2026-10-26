// SPDX-License-Identifier: MIT

package transport

import (
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	subs := &sync.Map{}
	subs.Store(client, true)
	clientSubs.Store("DEPARTED", subs)
	mdAwaiting.Store("DEPARTED", struct{}{})
	t.Cleanup(func() { clientSubs.Delete("DEPARTED"); mdAwaiting.Delete("DEPARTED") })

	unregisterClientWriter(client)
	if _, ok := subs.Load(client); ok {
		t.Fatal("subscription survived unregister")
	}
	if n, err := replaySubscriptions(&pooledConn{}, true); n != 0 || err != nil {
		t.Fatalf("departed symbol re-requested: n=%d err=%v", n, err)
	}
	if _, ok := mdAwaiting.Load("DEPARTED"); ok {
		t.Fatal("departed symbol still awaiting a snapshot")
	}
}
