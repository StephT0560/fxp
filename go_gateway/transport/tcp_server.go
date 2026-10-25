// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"log/slog"
	"net"
	"sync"
	"time"

	"go_gateway/protobuf"
	"go_gateway/startup"
	"go_gateway/tlsconfig"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func StartTCPServer() {
	if err := RunTCPServer(context.Background()); err != nil {
		log.Fatalf("❌ TCP server stopped: %v", err)
	}
}

func RunTCPServer(ctx context.Context) error {
	manager, err := tlsconfig.ServerFromEnv()
	if err != nil {
		return err
	}
	go manager.Watch(ctx, time.Second)
	return RunTCPServerWithTLS(ctx, manager)
}
func RunTCPServerWithTLS(ctx context.Context, manager *tlsconfig.Server) error {
	if err := InitPool(); err != nil {
		return err
	}
	listener, err := manager.Listen(startup.Address("FXP_GW_TCP_ADDR", "127.0.0.1:8082"))
	if err != nil {
		return err
	}
	defer listener.Close()
	log.Printf("TCP server listening on %s (TLS=%t)", listener.Addr(), manager.Enabled())
	serveTCPContext(ctx, listener)
	return nil
}

func serveTCP(listener net.Listener) {
	serveTCPContext(context.Background(), listener)
}

func serveTCPContext(ctx context.Context, listener net.Listener) {
	lim, conns := currentLimits(), tcpConnsP.Load()
	var handlers sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				if ctx.Err() != nil {
					shutdownClientWriters(shutdownTimeout())
				}
				handlers.Wait()
				return
			}
			log.Println("❌ Failed to accept TCP connection:", err)
			continue
		}
		if !conns.tryAcquire() {
			log.Printf("⛔ TCP connection limit (%d) reached — rejecting %s", lim.MaxConns, clientConn.RemoteAddr())
			clientConn.Close()
			continue
		}
		handlers.Add(1)
		writer := registerClientWriter(clientConn)
		go func() {
			defer handlers.Done()
			defer conns.release()
			if err := tlsconfig.Handshake(ctx, clientConn, lim.HandshakeTimeout); err != nil {
				unregisterClientWriter(clientConn)
				return
			}
			handleTCPClientWithWriter(clientConn, writer)
		}()
	}
}

func handleTCPClient(clientConn net.Conn) {
	handleTCPClientWithWriter(clientConn, registerClientWriter(clientConn))
}

func handleTCPClientWithWriter(clientConn net.Conn, writer *clientWriter) {
	defer unregisterClientWriter(clientConn)
	lim := currentLimits()
	sessionID := ""
	defer func() {
		if sessionID != "" {
			endSession(sessionID)
		}
	}()
	bucket := newTokenBucket(lim.RatePerSec, lim.RateBurst)
	authed := false
	// Logon must complete within the handshake timeout.
	_ = clientConn.SetReadDeadline(time.Now().Add(lim.HandshakeTimeout))
	for {
		maxFrame := lim.MaxFramePreAuth
		if authed {
			maxFrame = lim.MaxFrame
		}
		msgBuf, err := readFrame(clientConn, maxFrame)
		if err != nil {
			log.Printf("❌ Closing TCP client: %v", err)
			return
		}
		if !bucket.allow(time.Now()) {
			sendSessionError(clientConn, protobuf.ErrorType_RATE_LIMIT_EXCEEDED, "message rate limit exceeded")
			continue
		}
		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(msgBuf, msg); err != nil {
			log.Println("❌ Invalid FXPMessage from TCP client:", err)
			continue
		}
		if msg.Payload == nil {
			log.Println("⚠️ FXPMessage has no payload")
			continue
		}
		HandleFXPMessageAuthenticated(clientConn, msg, &sessionID)
		if !authed && sessionID != "" {
			authed = true
			_ = clientConn.SetReadDeadline(time.Time{})
		}
	}
}

func SendOrderToRust(clientConn net.Conn, orderData []byte) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(orderData, msg); err != nil {
		log.Printf("❌ SendOrderToRust: failed to decode order: %v", err)
		return
	}
	order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder)
	if !ok {
		log.Println("❌ SendOrderToRust: payload is not a NewOrder")
		return
	}
	orderID := order.NewOrder.OrderId
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendOrderToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	slog.Debug("registered order to TCP client", "order_id", orderID)
	SendFramedToCore(clientConn, frame, orderID, order.NewOrder.Sender, false)
}

func ForwardToTCPServer(wsConn *websocket.Conn, msg *protobuf.FXPMessage, frame []byte) {
	order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder)
	if !ok {
		log.Println("❌ ForwardToTCPServer: payload is not a NewOrder")
		return
	}
	orderID := order.NewOrder.OrderId
	slog.Debug("registered order to WebSocket client", "order_id", orderID)
	SendFramedToCore(wsConn, frame, orderID, order.NewOrder.Sender, false)
	slog.Debug("WebSocket order forwarded to FXP Core")
}

func forwardSubscriptionToCore(request *protobuf.MarketDataRequest) {
	// Filter out empty symbol strings — Rust would spawn a ghost order book task for them
	filtered := make([]string, 0, len(request.Symbols))
	for _, s := range request.Symbols {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return
	}

	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{
			MarketDataRequest: &protobuf.MarketDataRequest{
				Symbols: filtered,
			},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ Failed to encode MarketDataRequest for Rust Core: %v", err)
		return
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	pc := acquire()
	defer release(pc)
	if err := pc.write(frame); err != nil {
		log.Printf("❌ Failed to send MarketDataRequest to Rust Core: %v", err)
		return
	}
	slog.Debug("MarketDataRequest forwarded to FXP Core", "symbols", filtered)
}

// SendCancelToRust forwards an OrderCancelRequest or OrderCancelReplaceRequest
// to Rust Core via the connection pool. The original order_id is registered in
// orderClientMap so the resulting ExecutionReport is routed back correctly.
func SendCancelToRust(clientConn net.Conn, rawMsg []byte, orderID string) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(rawMsg, msg); err != nil {
		log.Printf("❌ SendCancelToRust: decode failed: %v", err)
		return
	}
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendCancelToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	slog.Debug("sending cancel to FXP Core", "order_id", orderID)
	SendFramedToCore(clientConn, frame, orderID, orderOwner(msg), true)
}

// SendCancelWSToRust forwards a cancel/replace from a WebSocket client.
func SendCancelWSToRust(wsConn *websocket.Conn, rawMsg []byte, orderID string) {
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(rawMsg, msg); err != nil {
		log.Printf("❌ SendCancelWSToRust: decode failed: %v", err)
		return
	}
	encoded, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ SendCancelWSToRust: re-encode failed: %v", err)
		return
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	slog.Debug("sending WebSocket cancel to FXP Core", "order_id", orderID)
	SendFramedToCore(wsConn, frame, orderID, orderOwner(msg), true)
}
