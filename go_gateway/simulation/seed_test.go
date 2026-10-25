// SPDX-License-Identifier: MIT
package main

import (
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

// Capture order content, excluding wall-clock timestamps. This checks seeded
// order generation, not wire compatibility (covered by the Rust-core smoke).
type orderCapture struct {
	net.Conn
	orders []*protobuf.NewOrderSingle
}

func (c *orderCapture) Write(frame []byte) (int, error) {
	msg := new(protobuf.FXPMessage)
	if err := proto.Unmarshal(frame[4:], msg); err != nil {
		return 0, err
	}
	if order := msg.GetNewOrder(); order != nil {
		order.Timestamp = nil
		c.orders = append(c.orders, order)
	}
	return len(frame), nil
}

func seededOrders(seed int64, kind ParticipantType) []*protobuf.NewOrderSingle {
	capture := new(orderCapture)
	p := &Participant{
		traderID: "seed-test", ptype: kind, conn: capture,
		symbols: []*SymbolState{newSymbolState("AAPL", 18500, 15), newSymbolState("MSFT", 37500, 20)},
		rng:     rand.New(rand.NewSource(seed)), stats: new(SimStats),
		pending: make(map[string]time.Time), symbolByOrder: make(map[string]*SymbolState), stopOrderIDs: make(map[string]bool),
		phaseMu: new(sync.RWMutex),
	}
	for range 30 {
		p.sendOrder(PhaseContinuous)
	}
	return capture.orders
}

func TestSeedDeterminesOrders(t *testing.T) {
	for _, kind := range []ParticipantType{TypeMarketMaker, TypeRetail, TypeHedgeFund} {
		a, b, c := seededOrders(7, kind), seededOrders(7, kind), seededOrders(8, kind)
		if len(a) == 0 || len(a) != len(b) {
			t.Fatal("missing seeded orders")
		}
		different := len(a) != len(c)
		for i := range a {
			if !proto.Equal(a[i], b[i]) {
				t.Fatalf("%s order %d differs for seed 7", kind, i)
			}
			if i < len(c) && !proto.Equal(a[i], c[i]) {
				different = true
			}
		}
		if !different {
			t.Fatalf("%s seed did not affect orders", kind)
		}
	}
}
