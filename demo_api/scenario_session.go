// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"go_gateway/protobuf"
	"go_gateway/test_clients/devjwt"
	"go_gateway/tlsconfig"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type scenarioSession struct {
	run      *scenarioRun
	actor    string
	conn     net.Conn
	mu       sync.Mutex
	messages []*protobuf.FXPMessage
	wake     chan struct{}
	done     chan struct{}
	readErr  error
	stop     func() bool
}

func (r *scenarioRun) connect(ctx context.Context, actor string, cod bool) (*scenarioSession, error) {
	return r.connectToken(ctx, actor, cod, func() (string, error) {
		return devjwt.GenerateWithTTL(r.cfg.secret, r.ownerPrefix+actor, 2*time.Minute)
	})
}
func (r *scenarioRun) connectToken(ctx context.Context, actor string, cod bool, mint func() (string, error)) (*scenarioSession, error) {
	options, e := tlsconfig.ClientFromEnv("FXP_CLIENT_")
	if e != nil {
		return nil, errors.New("invalid scenario client TLS configuration")
	}
	conn, e := options.Dial(ctx, r.cfg.simGateway, 5*time.Second)
	if e != nil {
		return nil, fmt.Errorf("%s: gateway connection failed", actor)
	}
	s := &scenarioSession{run: r, actor: actor, conn: conn, wake: make(chan struct{}), done: make(chan struct{})}
	s.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	token, e := mint()
	if e == nil {
		e = writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: token, CancelOnDisconnect: cod}}})
	}
	if e == nil {
		var ack *protobuf.FXPMessage
		ack, e = readFrame(conn)
		if e == nil && (ack.GetLogonAck() == nil || !ack.GetLogonAck().Accepted) {
			e = errors.New("logon rejected")
		}
	}
	if e != nil {
		s.stop()
		_ = conn.Close()
		return nil, fmt.Errorf("%s: gateway authentication failed", actor)
	}
	_ = conn.SetDeadline(time.Time{})
	go s.read()
	return s, nil
}
func (s *scenarioSession) read() {
	defer close(s.done)
	for {
		msg, e := readFrame(s.conn)
		s.mu.Lock()
		if e != nil {
			s.readErr = e
			close(s.wake)
			s.mu.Unlock()
			return
		}
		s.messages = append(s.messages, msg)
		close(s.wake)
		s.wake = make(chan struct{})
		s.mu.Unlock()
		s.run.publish(s.actor, msg)
	}
}
func (r *scenarioRun) publish(actor string, m *protobuf.FXPMessage) {
	// Explicit allow-list: no Logon/JWT or unknown message is ever streamed.
	switch m.Payload.(type) {
	case *protobuf.FXPMessage_ExecutionReport, *protobuf.FXPMessage_MarketDataSnapshot, *protobuf.FXPMessage_MarketDataIncremental, *protobuf.FXPMessage_TradeCaptureReport, *protobuf.FXPMessage_TradingSessionStatus, *protobuf.FXPMessage_AuctionInfo, *protobuf.FXPMessage_OrderMassCancelReport, *protobuf.FXPMessage_OrderStatusResponse:
		raw, e := protojson.Marshal(m)
		if e == nil {
			r.emit(scenarioEvent{Kind: "frame", Actor: actor, Frame: raw})
		}
	}
}
func (s *scenarioSession) close() { s.stop(); _ = s.conn.Close(); <-s.done }
func (s *scenarioSession) send(m *protobuf.FXPMessage) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return writeFrame(s.conn, m)
}
func (s *scenarioSession) mark() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.messages) }
func (s *scenarioSession) wait(ctx context.Context, from int, description string, predicate func(*protobuf.FXPMessage) bool) (*protobuf.FXPMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for {
		s.mu.Lock()
		for i := from; i < len(s.messages); i++ {
			if predicate(s.messages[i]) {
				m := s.messages[i]
				s.mu.Unlock()
				return m, nil
			}
		}
		wake := s.wake
		err := s.readErr
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("%s: connection closed before %s", s.actor, description)
		}
		select {
		case <-ctx.Done():
			if s.run.ctx.Err() != nil {
				return nil, s.run.ctx.Err()
			}
			return nil, fmt.Errorf("%s: timed out waiting for %s", s.actor, description)
		case <-wake:
		}
	}
}
func (s *scenarioSession) report(ctx context.Context, id string, kind protobuf.ExecutionType) (*protobuf.ExecutionReport, error) {
	msg, e := s.wait(ctx, 0, fmt.Sprintf("order %s execution %s", id, kind), func(m *protobuf.FXPMessage) bool {
		v := m.GetExecutionReport()
		return v != nil && (v.OrderId == id || v.ClientOrderId == id) && (v.ExecutionType == kind || v.ExecutionType == protobuf.ExecutionType_EXEC_REJECTED)
	})
	if e != nil {
		return nil, e
	}
	v := msg.GetExecutionReport()
	if v.ExecutionType != kind {
		return nil, fmt.Errorf("order rejected: %s", v.RejectReason)
	}
	return v, nil
}
func (r *scenarioRun) order(s *scenarioSession, label string, side protobuf.OrderSide, qty int32, price int64, change func(*protobuf.NewOrderSingle)) (string, error) {
	id := r.ID + "-" + label
	o := &protobuf.NewOrderSingle{OrderId: id, ClientOrderId: id, Symbol: r.Symbol, Side: side, Quantity: qty, Price: price, OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_GTC, StpMode: protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_NONE, Timestamp: timestamppb.Now()}
	if change != nil {
		change(o)
	}
	if e := s.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: o}}); e != nil {
		return id, errors.New("could not send order " + label)
	}
	_, e := s.report(r.ctx, id, protobuf.ExecutionType_EXEC_NEW)
	return id, e
}
func (r *scenarioRun) snapshot(ctx context.Context, s *scenarioSession) (*protobuf.MarketDataSnapshot, error) {
	mark := s.mark()
	if e := s.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{Symbols: []string{r.Symbol}}}}); e != nil {
		return nil, e
	}
	msg, e := s.wait(ctx, mark, "fresh book snapshot", func(m *protobuf.FXPMessage) bool {
		return m.GetMarketDataSnapshot() != nil && m.GetMarketDataSnapshot().Symbol == r.Symbol
	})
	if e != nil {
		return nil, e
	}
	return msg.GetMarketDataSnapshot(), nil
}
func (r *scenarioRun) cancelAll(ctx context.Context, s *scenarioSession) error {
	mark := s.mark()
	id := r.ID + "-cleanup-" + s.actor
	if e := s.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancel{OrderMassCancel: &protobuf.OrderMassCancelRequest{ClientRequestId: id, Symbol: r.Symbol}}}); e != nil {
		return e
	}
	msg, e := s.wait(ctx, mark, "mass-cancel completion", func(m *protobuf.FXPMessage) bool {
		return m.GetOrderMassCancelReport() != nil && m.GetOrderMassCancelReport().ClientRequestId == id
	})
	if e != nil {
		return e
	}
	if msg.GetOrderMassCancelReport().RejectReason != "" {
		return errors.New("cleanup mass cancel rejected")
	}
	return nil
}
func (r *scenarioRun) bootstrap() (a, b, observer *scenarioSession, err error) {
	for _, actor := range []string{"alice", "bob", "observer"} {
		s, e := r.connect(r.ctx, actor, false)
		if e != nil {
			return nil, nil, nil, e
		}
		r.sessions = append(r.sessions, s)
	}
	a, b, observer = r.sessions[0], r.sessions[1], r.sessions[2]
	_, err = r.order(a, "admission", protobuf.OrderSide_BUY, 1, r.price/2, nil)
	if err != nil {
		return
	}
	_, err = r.snapshot(r.ctx, observer)
	if err != nil {
		return
	}
	err = r.cancelAll(r.ctx, a)
	if err == nil {
		var snap *protobuf.MarketDataSnapshot
		snap, err = r.snapshot(r.ctx, observer)
		if err == nil {
			err = assertScenario(len(snap.BidLevels)+len(snap.AskLevels) == 0, "setup expected an empty fresh book")
		}
	}
	return
}
func (r *scenarioRun) cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	// Kill/reap children before removing stores. Reverse order preserves dependencies.
	for i := len(r.cleanupExtra) - 1; i >= 0; i-- {
		if e := r.cleanupExtra[i](); e != nil {
			errs = append(errs, e)
		}
	}
	actors := map[string]bool{}
	for _, s := range r.sessions {
		s.close()
		actors[s.actor] = true
	}
	// Reconnect run-scoped identities so cancellation works after abrupt TCP loss.
	for _, owner := range r.externalOwners {
		s, e := r.connectToken(ctx, "fix-user", false, func() (string, error) { return devjwt.GenerateWithTTL(r.cfg.secret, owner, time.Minute) })
		if e == nil {
			e = r.cancelAll(ctx, s)
			s.close()
		}
		if e != nil {
			errs = append(errs, e)
		}
	}
	for actor := range actors {
		if actor == "observer" {
			continue
		}
		s, e := r.connect(ctx, actor, false)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		if e = r.cancelAll(ctx, s); e != nil {
			errs = append(errs, e)
		}
		s.close()
	}
	if len(actors) > 0 {
		s, e := r.connect(ctx, "observer", false)
		if e == nil {
			snap, err := r.snapshot(ctx, s)
			if err != nil {
				errs = append(errs, err)
			} else if len(snap.BidLevels)+len(snap.AskLevels) != 0 {
				errs = append(errs, errors.New("scenario book still contains orders"))
			}
			s.close()
		} else {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
