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
	data bytes.Buffer
}

func (c *auctionConn) Write(p []byte) (int, error) { return c.data.Write(p) }

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
					subscribers, others := &sync.Map{}, &sync.Map{}
					subscribers.Store(subscribed, true)
					others.Store(other, true)
					clientSubs.Store("AUCT", subscribers)
					clientSubs.Store("OTHER", others)
					t.Cleanup(func() { clientSubs.Delete("AUCT"); clientSubs.Delete("OTHER") })
					router(message)
					raw := subscribed.data.Bytes()
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
					if other.data.Len() != 0 {
						t.Fatal("payload leaked to another symbol")
					}
					clientSubs.Delete("AUCT")
					router(message)
					if subscribed.data.Len() != len(raw) || other.data.Len() != 0 {
						t.Fatal("unsubscribed payload was delivered")
					}
				})
			}
		})
	}
}
