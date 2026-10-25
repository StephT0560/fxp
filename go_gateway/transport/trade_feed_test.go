// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func tradeFeedMessage() *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_TradeCaptureReport{
		TradeCaptureReport: &protobuf.TradeCaptureReport{
			Header:  &protobuf.FXPHeader{ProtocolVersion: "2.5"},
			TradeId: "MDSYM:7", Symbol: "MDSYM", TradePrice: 1010, TradeQuantity: 30,
			Timestamp:     timestamppb.New(time.Date(2024, 1, 2, 3, 4, 5, 678_000_000, time.UTC)),
			AggressorSide: protobuf.OrderSide_SELL, Seq: 7, IsAuction: true,
		},
	}}
}

func TestTradeCaptureReportsFollowSymbolSubscriptions(t *testing.T) {
	for name, router := range map[string]func(*protobuf.FXPMessage){
		"pool":   routeFromCore,
		"common": func(msg *protobuf.FXPMessage) { HandleFXPMessage(nil, msg) },
	} {
		t.Run(name, func(t *testing.T) {
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
			clientSubs.Store("MDSYM", subscribers)
			clientSubs.Store("OTHER", others)
			t.Cleanup(func() { clientSubs.Delete("MDSYM"); clientSubs.Delete("OTHER") })

			message := tradeFeedMessage()
			router(message)
			waitUntil(t, func() bool { return subscribed.size() >= 4 })
			raw := subscribed.bytes()
			if int(binary.BigEndian.Uint32(raw[:4])) != len(raw)-4 {
				t.Fatal("invalid TCP frame length")
			}
			got := &protobuf.FXPMessage{}
			if err := proto.Unmarshal(raw[4:], got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(message, got) {
				t.Fatalf("trade payload changed: got %v, want %v", got, message)
			}
			if other.size() != 0 {
				t.Fatal("trade was delivered to a different symbol")
			}
		})
	}
}

func TestTradeCaptureReportReachesOnlySubscribedWebSocket(t *testing.T) {
	serverConns := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConns <- conn
		}
	}))
	t.Cleanup(server.Close)
	dial := func() (*websocket.Conn, *websocket.Conn) {
		client, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
		if err != nil {
			t.Fatal(err)
		}
		var serverConn *websocket.Conn
		select {
		case serverConn = <-serverConns:
		case <-time.After(time.Second):
			t.Fatal("WebSocket handler did not upgrade")
		}
		return client, serverConn
	}
	subscribed, subscribedServer := dial()
	unsubscribed, unsubscribedServer := dial()
	writer := registerClientWriter(subscribedServer)
	t.Cleanup(func() {
		unregisterClientWriter(subscribedServer)
		<-writer.finished
		_ = subscribedServer.Close()
		_ = unsubscribedServer.Close()
		if value, ok := clientSubs.Load("MDSYM"); ok {
			subs := value.(*sync.Map)
			subs.Delete(subscribedServer)
			subs.Delete(unsubscribedServer)
			hasSubscriber := false
			subs.Range(func(_, _ any) bool { hasSubscriber = true; return false })
			if !hasSubscriber {
				clientSubs.Delete("MDSYM")
			}
		}
		_ = subscribed.Close()
		_ = unsubscribed.Close()
	})
	clients, _ := clientSubs.LoadOrStore("MDSYM", &sync.Map{})
	clients.(*sync.Map).Store(subscribedServer, true)

	message := tradeFeedMessage()
	routeFromCore(message)
	_ = subscribed.SetReadDeadline(time.Now().Add(time.Second))
	messageType, raw, err := subscribed.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.BinaryMessage {
		t.Fatalf("WebSocket message type = %d, want binary", messageType)
	}
	got := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(raw, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(message, got) {
		t.Fatalf("WebSocket trade payload changed: got %v, want %v", got, message)
	}

	_ = subscribed.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := subscribed.ReadMessage(); !isTimeout(err) {
		t.Fatalf("subscribed WebSocket received an extra message: %v", err)
	}
	_ = unsubscribed.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := unsubscribed.ReadMessage(); !isTimeout(err) {
		t.Fatalf("unsubscribed WebSocket received trade data: %v", err)
	}
}
