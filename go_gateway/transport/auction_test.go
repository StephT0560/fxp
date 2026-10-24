// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

type auctionConn struct {
	net.Conn
	mu   sync.Mutex
	data bytes.Buffer
}

func (c *auctionConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.Write(p)
}

func (c *auctionConn) Close() error { return nil }

func (c *auctionConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data.Bytes()...)
}

func (c *auctionConn) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.Len()
}

func TestAuctionFramesFollowSymbolSubscriptions(t *testing.T) {
	messages := []*protobuf.FXPMessage{
		{Payload: &protobuf.FXPMessage_TradingSessionStatus{TradingSessionStatus: &protobuf.TradingSessionStatus{
			Symbol: "AUCT", Phase: protobuf.TradingPhase_OPENING_CALL,
		}}},
		{Payload: &protobuf.FXPMessage_AuctionInfo{AuctionInfo: &protobuf.AuctionInfo{
			Symbol: "AUCT", Phase: protobuf.TradingPhase_CLOSING_CALL,
			IndicativePrice: 100, IndicativeVolume: 5, ImbalanceQuantity: 3,
			ImbalanceSide: protobuf.OrderSide_BUY, ReferencePrice: 99,
		}}},
	}
	for name, router := range map[string]func(*protobuf.FXPMessage){
		"pool":   routeFromCore,
		"common": func(m *protobuf.FXPMessage) { HandleFXPMessage(nil, m) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, message := range messages {
				t.Run(string(message.ProtoReflect().WhichOneof(message.ProtoReflect().Descriptor().Oneofs().Get(0)).Name()), func(t *testing.T) {
					subscribed, other := &auctionConn{}, &auctionConn{}
					subscriberWriter := registerClientWriter(subscribed)
					otherWriter := registerClientWriter(other)
					t.Cleanup(func() {
						unregisterClientWriter(subscribed)
						unregisterClientWriter(other)
						<-subscriberWriter.finished
						<-otherWriter.finished
					})
					subscribers, others := &sync.Map{}, &sync.Map{}
					subscribers.Store(subscribed, true)
					others.Store(other, true)
					clientSubs.Store("AUCT", subscribers)
					clientSubs.Store("OTHER", others)
					t.Cleanup(func() { clientSubs.Delete("AUCT"); clientSubs.Delete("OTHER") })
					router(message)
					waitUntil(t, func() bool { return subscribed.size() >= 4 })
					raw := subscribed.bytes()
					if len(raw) < 4 {
						t.Fatal("symbol subscriber received no framed payload")
					}
					if int(binary.BigEndian.Uint32(raw[:4])) != len(raw)-4 {
						t.Fatal("invalid TCP frame length")
					}
					got := &protobuf.FXPMessage{}
					if err := proto.Unmarshal(raw[4:], got); err != nil {
						t.Fatal(err)
					}
					if !proto.Equal(message, got) {
						t.Fatalf("payload changed: got %v, want %v", got, message)
					}
					if other.size() != 0 {
						t.Fatal("payload leaked to another symbol")
					}
					clientSubs.Delete("AUCT")
					router(message)
					if subscribed.size() != len(raw) || other.size() != 0 {
						t.Fatal("unsubscribed payload was delivered")
					}
				})
			}
		})
	}
}
