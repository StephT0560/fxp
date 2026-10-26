// SPDX-License-Identifier: MIT

package transport

import (
	"io"
	"net"
	"sync/atomic"
	"testing"

	"go_gateway/protobuf"
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
