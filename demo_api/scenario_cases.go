// SPDX-License-Identifier: MIT
package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go_gateway/protobuf"
	"go_gateway/test_clients/devjwt"
)

func executeScenario(r *scenarioRun) error {
	var a, b, o *scenarioSession
	if e := r.step("Create a fresh isolated book", "ladder", func() (string, error) {
		var e error
		a, b, o, e = r.bootstrap()
		return "Fresh book admitted, subscribed and empty", e
	}); e != nil {
		return e
	}
	switch r.Scenario {
	case "opening-auction":
		return r.auction(a, b, o)
	case "stop-cascade":
		return r.cascade(a, b, o)
	case "stp-modes":
		return r.stp(a, o)
	case "iceberg-priority":
		return r.iceberg(a, b, o)
	case "cancel-on-disconnect":
		return r.disconnect(a, o)
	case "fix-recovery":
		return r.fixRecovery(o)
	}
	return fmt.Errorf("unknown scenario %q", r.Scenario)
}
func (r *scenarioRun) trades(s *scenarioSession, from int) []*protobuf.TradeCaptureReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*protobuf.TradeCaptureReport{}
	for _, m := range s.messages[from:] {
		if t := m.GetTradeCaptureReport(); t != nil && t.Symbol == r.Symbol {
			out = append(out, t)
		}
	}
	return out
}
func (r *scenarioRun) waitTrades(s *scenarioSession, from, n int) ([]*protobuf.TradeCaptureReport, error) {
	_, e := s.wait(r.ctx, from, fmt.Sprintf("%d public prints", n), func(m *protobuf.FXPMessage) bool { return len(r.tradesUnlocked(s, from)) >= n })
	if e != nil {
		return nil, e
	}
	return r.trades(s, from), nil
}

// wait predicates run while the session mutex is held.
func (r *scenarioRun) tradesUnlocked(s *scenarioSession, from int) []*protobuf.TradeCaptureReport {
	out := []*protobuf.TradeCaptureReport{}
	for _, m := range s.messages[from:] {
		if t := m.GetTradeCaptureReport(); t != nil && t.Symbol == r.Symbol {
			out = append(out, t)
		}
	}
	return out
}
func (r *scenarioRun) emptyBook(o *scenarioSession) error {
	s, e := r.snapshot(r.ctx, o)
	if e != nil {
		return e
	}
	return assertScenario(len(s.BidLevels)+len(s.AskLevels) == 0, "expected empty book; got %d bids and %d asks", len(s.BidLevels), len(s.AskLevels))
}
func (r *scenarioRun) auction(a, b, o *scenarioSession) error {
	if e := r.step("Open a call on this symbol only", "ladder", func() (string, error) {
		op, e := r.operator()
		if e != nil {
			return "", e
		}
		r.sessions = append(r.sessions, op)
		mark := o.mark()
		id := r.ID + "-call"
		e = op.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControl{SessionControl: &protobuf.SessionControl{RequestId: id, Symbol: r.Symbol, OpeningCallSeconds: 4}}})
		if e != nil {
			return "", e
		}
		msg, e := op.wait(r.ctx, 0, "one-symbol call report", func(m *protobuf.FXPMessage) bool {
			return m.GetSessionControlReport() != nil && m.GetSessionControlReport().RequestId == id
		})
		if e != nil {
			return "", e
		}
		rep := msg.GetSessionControlReport()
		if e = assertScenario(rep.RejectReason == "" && rep.SymbolsAffected == 1, "expected exactly one symbol affected; got %d (%s)", rep.SymbolsAffected, rep.RejectReason); e != nil {
			return "", e
		}
		_, e = o.wait(r.ctx, mark, "OPENING_CALL", func(m *protobuf.FXPMessage) bool {
			return m.GetTradingSessionStatus() != nil && m.GetTradingSessionStatus().Phase == protobuf.TradingPhase_OPENING_CALL
		})
		return "Exactly one fresh symbol entered OPENING_CALL", e
	}); e != nil {
		return e
	}
	var buy, sell string
	mark := o.mark()
	if e := r.step("Cross at-open orders without continuous fills", "blotter", func() (string, error) {
		atOpen := func(v *protobuf.NewOrderSingle) { v.TimeInForce = protobuf.TimeInForce_AT_OPEN }
		var e error
		buy, e = r.order(a, "auction-buy", protobuf.OrderSide_BUY, r.qty, r.price, atOpen)
		if e != nil {
			return "", e
		}
		sell, e = r.order(b, "auction-sell", protobuf.OrderSide_SELL, r.qty, r.price, atOpen)
		if e != nil {
			return "", e
		}
		snap, e := r.snapshot(r.ctx, o)
		if e != nil {
			return "", e
		}
		e = assertScenario(len(snap.BidLevels) == 1 && len(snap.AskLevels) == 1 && snap.BidLevels[0].Price == r.price && snap.AskLevels[0].Price == r.price && snap.BidLevels[0].Size == r.qty && snap.AskLevels[0].Size == r.qty && len(r.trades(o, mark)) == 0, "expected crossed resting book and no print during call")
		return fmt.Sprintf("Both at-open orders rest: quantity=%d price_minor=%d; no prints", r.qty, r.price), e
	}); e != nil {
		return e
	}
	return r.step("Uncross into one public auction print", "tape", func() (string, error) {
		_, e := r.waitTrades(o, mark, 1)
		if e != nil {
			return "", e
		}
		for _, v := range []struct {
			s  *scenarioSession
			id string
		}{{a, buy}, {b, sell}} {
			rep, e := v.s.report(r.ctx, v.id, protobuf.ExecutionType_EXEC_FILLED)
			if e != nil {
				return "", e
			}
			if e = assertScenario(rep.Auction && rep.CumQuantity == r.qty && rep.LeavesQuantity == 0, "expected full auction fill for both orders"); e != nil {
				return "", e
			}
		}
		if e = r.emptyBook(o); e != nil {
			return "", e
		}
		ts := r.trades(o, mark)
		e = assertScenario(len(ts) == 1 && ts[0].IsAuction && ts[0].TradePrice == r.price && ts[0].TradeQuantity == r.qty, "expected exactly one auction print at the uncross price")
		return fmt.Sprintf("One auction print; quantity=%d price_minor=%d; both filled; book empty", r.qty, r.price), e
	})
}
func (r *scenarioRun) operator() (*scenarioSession, error) {
	return r.connectToken(r.ctx, "operator", false, func() (string, error) {
		return devjwt.GenerateOperator(r.cfg.secret, r.ownerPrefix+"operator", time.Minute)
	})
}
func (r *scenarioRun) cascade(a, b, o *scenarioSession) error {
	var first, second string
	if e := r.step("Park two stops above successive ask levels", "ladder", func() (string, error) {
		for i := 0; i < 3; i++ {
			if _, e := r.order(a, fmt.Sprintf("ask-%d", i), protobuf.OrderSide_SELL, 1, r.price+int64(i), nil); e != nil {
				return "", e
			}
		}
		var e error
		first, e = r.order(b, "stop-1", protobuf.OrderSide_BUY, 1, 0, func(v *protobuf.NewOrderSingle) { v.OrderType = protobuf.OrderType_STOP; v.StopPrice = r.price })
		if e != nil {
			return "", e
		}
		second, e = r.order(b, "stop-2", protobuf.OrderSide_BUY, 1, 0, func(v *protobuf.NewOrderSingle) { v.OrderType = protobuf.OrderType_STOP; v.StopPrice = r.price + 1 })
		if e != nil {
			return "", e
		}
		snap, e := r.snapshot(r.ctx, o)
		if e != nil {
			return "", e
		}
		if e = assertScenario(len(snap.AskLevels) == 3 && len(snap.BidLevels) == 0, "expected three asks and invisible parked stops"); e != nil {
			return "", e
		}
		for i, level := range snap.AskLevels {
			if e = assertScenario(level.Price == r.price+int64(i) && level.Size == 1, "ask level %d differs from seeded fixture", i+1); e != nil {
				return "", e
			}
		}
		return "Three ask levels; stop-1 at first price, stop-2 one tick higher", nil
	}); e != nil {
		return e
	}
	mark := o.mark()
	reportsFrom := b.mark()
	return r.step("Each print triggers the next stop in order", "tape", func() (string, error) {
		if _, e := r.order(b, "trigger", protobuf.OrderSide_BUY, 1, r.price, nil); e != nil {
			return "", e
		}
		_, e := r.waitTrades(o, mark, 3)
		if e != nil {
			return "", e
		}
		for _, id := range []string{first, second} {
			rep, e := b.report(r.ctx, id, protobuf.ExecutionType_EXEC_FILLED)
			if e != nil {
				return "", e
			}
			if e = assertScenario(rep.CumQuantity == 1, "stop must fill once"); e != nil {
				return "", e
			}
		}
		if e = r.emptyBook(o); e != nil {
			return "", e
		}
		ts := r.trades(o, mark)
		if e = assertScenario(len(ts) == 3, "expected exactly 3 prints, got %d", len(ts)); e != nil {
			return "", e
		}
		for i, t := range ts {
			if e = assertScenario(t.TradePrice == r.price+int64(i) && t.TradeQuantity == 1 && t.Seq == uint64(i+1), "print %d: expected price %d, qty 1, seq %d; got %d/%d/%d", i+1, r.price+int64(i), i+1, t.TradePrice, t.TradeQuantity, t.Seq); e != nil {
				return "", e
			}
		}
		b.mu.Lock()
		var ordered []*protobuf.ExecutionReport
		for _, m := range b.messages[reportsFrom:] {
			rep := m.GetExecutionReport()
			if rep != nil && rep.ExecutionType == protobuf.ExecutionType_EXEC_FILLED && (rep.OrderId == first || rep.OrderId == second) {
				ordered = append(ordered, rep)
			}
		}
		b.mu.Unlock()
		if e = sortScenarioReports(ordered); e != nil {
			return "", e
		}
		var fires []string
		for _, rep := range ordered {
			fires = append(fires, strings.TrimPrefix(rep.OrderId, r.ID+"-"))
		}

		e = assertScenario(strings.Join(fires, ",") == "stop-1,stop-2", "expected stop-1,stop-2 fire order; got %v", fires)
		return fmt.Sprintf("Prints %d→%d→%d; stop-1 then stop-2; seq 1,2,3; book empty", r.price, r.price+1, r.price+2), e
	})
}
func (r *scenarioRun) stp(a, o *scenarioSession) error {
	modes := []struct {
		name       string
		mode       protobuf.SelfTradePrevention
		rest, aggr protobuf.ExecutionType
		bid, ask   int32
		prints     int
	}{
		{"NONE", protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_NONE, protobuf.ExecutionType_EXEC_FILLED, protobuf.ExecutionType_EXEC_FILLED, 1, 0, 1},
		{"CANCEL_NEWEST", protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_NEWEST, 0, protobuf.ExecutionType_EXEC_CANCELED, r.qty, 0, 0},
		{"CANCEL_OLDEST", protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_OLDEST, protobuf.ExecutionType_EXEC_CANCELED, 0, 0, r.qty - 1, 0},
		{"CANCEL_BOTH", protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_CANCEL_BOTH, protobuf.ExecutionType_EXEC_CANCELED, protobuf.ExecutionType_EXEC_CANCELED, 0, 0, 0},
		{"DECREMENT_AND_CANCEL", protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_DECREMENT_AND_CANCEL, protobuf.ExecutionType_EXEC_RESTATED, protobuf.ExecutionType_EXEC_CANCELED, 1, 0, 0},
	}
	for _, v := range modes {
		if e := r.step(v.name+": identical setup, different STP outcome", "blotter", func() (string, error) {
			if e := r.cancelAll(r.ctx, a); e != nil {
				return "", e
			}
			mark := o.mark()
			rest, e := r.order(a, v.name+"-rest", protobuf.OrderSide_BUY, r.qty, r.price, nil)
			if e != nil {
				return "", e
			}
			aggr, e := r.order(a, v.name+"-aggr", protobuf.OrderSide_SELL, r.qty-1, r.price, func(order *protobuf.NewOrderSingle) { order.StpMode = v.mode })
			if e != nil {
				return "", e
			}
			for _, check := range []struct {
				id   string
				kind protobuf.ExecutionType
			}{{rest, v.rest}, {aggr, v.aggr}} {
				if check.kind != 0 {
					if _, e = a.report(r.ctx, check.id, check.kind); e != nil {
						return "", e
					}
				}
			}
			if v.prints > 0 {
				if _, e = r.waitTrades(o, mark, v.prints); e != nil {
					return "", e
				}
			}
			snap, e := r.snapshot(r.ctx, o)
			if e != nil {
				return "", e
			}
			bid, ask := int32(0), int32(0)
			for _, l := range snap.BidLevels {
				bid += l.Size
			}
			for _, l := range snap.AskLevels {
				ask += l.Size
			}
			count := len(r.trades(o, mark))
			if e = assertScenario(bid == v.bid && ask == v.ask && count == v.prints, "%s expected bid=%d ask=%d prints=%d; got %d/%d/%d", v.name, v.bid, v.ask, v.prints, bid, ask, count); e != nil {
				return "", e
			}
			return fmt.Sprintf("%s: same owner/price, resting=%d aggressing=%d; bid=%d ask=%d prints=%d", v.name, r.qty, r.qty-1, bid, ask, count), nil
		}); e != nil {
			return e
		}
	}
	return nil
}
func (r *scenarioRun) iceberg(a, b, o *scenarioSession) error {
	var ice, peer string
	if e := r.step("Rest an iceberg before a fully displayed peer", "ladder", func() (string, error) {
		var e error
		ice, e = r.order(a, "iceberg", protobuf.OrderSide_SELL, 6, r.price, func(v *protobuf.NewOrderSingle) { v.DisplayQuantity = 2 })
		if e != nil {
			return "", e
		}
		peer, e = r.order(a, "peer", protobuf.OrderSide_SELL, 2, r.price, nil)
		if e != nil {
			return "", e
		}
		snap, e := r.snapshot(r.ctx, o)
		if e != nil {
			return "", e
		}
		return "Iceberg total 6/display 2, then peer 2; visible ask 4", assertScenario(len(snap.AskLevels) == 1 && snap.AskLevels[0].Size == 4, "expected displayed size 4")
	}); e != nil {
		return e
	}
	from := a.mark()
	mark := o.mark()
	return r.step("Consume a slice, then the peer, then the replenished slice", "blotter", func() (string, error) {
		if _, e := r.order(b, "sweep", protobuf.OrderSide_BUY, 5, r.price, nil); e != nil {
			return "", e
		}
		if _, e := a.report(r.ctx, peer, protobuf.ExecutionType_EXEC_FILLED); e != nil {
			return "", e
		}
		if _, e := r.waitTrades(o, mark, 3); e != nil {
			return "", e
		}
		snap, e := r.snapshot(r.ctx, o)
		if e != nil {
			return "", e
		}
		_, e = a.wait(r.ctx, from, "three iceberg/peer fill reports", func(m *protobuf.FXPMessage) bool {
			n := 0
			for _, v := range a.messages[from:] {
				if rep := v.GetExecutionReport(); rep != nil && rep.ExecutionType == protobuf.ExecutionType_EXEC_FILLED {
					n++
				}
			}
			return n == 3
		})
		if e != nil {
			return "", e
		}
		a.mu.Lock()
		var ordered []*protobuf.ExecutionReport
		for _, m := range a.messages[from:] {
			if rep := m.GetExecutionReport(); rep != nil && rep.ExecutionType == protobuf.ExecutionType_EXEC_FILLED {
				ordered = append(ordered, rep)
			}
		}
		a.mu.Unlock()
		if e = sortScenarioReports(ordered); e != nil {
			return "", e
		}
		var ids []string
		var qty []int32
		for _, rep := range ordered {
			ids = append(ids, rep.OrderId)
			qty = append(qty, rep.ExecutedQuantity)
		}

		e = assertScenario(len(ids) == 3 && ids[0] == ice && ids[1] == peer && ids[2] == ice && qty[0] == 2 && qty[1] == 2 && qty[2] == 1 && len(snap.AskLevels) == 1 && snap.AskLevels[0].Size == 1, "expected iceberg(2), peer(2), iceberg(1), visible residual 1; got fill quantities %v, ids=%v, ask=%v", qty, ids, snap.AskLevels)
		return "Fill order iceberg(2) → peer(2) → iceberg(1); replenishment lost priority; visible residual 1", e
	})
}
func (r *scenarioRun) disconnect(_ *scenarioSession, o *scenarioSession) error {
	var s *scenarioSession
	var ids []string
	if e := r.step("Rest two orders with cancel-on-disconnect enabled", "ladder", func() (string, error) {
		var e error
		s, e = r.connect(r.ctx, "cod-user", true)
		if e != nil {
			return "", e
		}
		r.sessions = append(r.sessions, s)
		for i := 0; i < 2; i++ {
			id, e := r.order(s, fmt.Sprintf("cod-%d", i), protobuf.OrderSide_BUY, r.qty, r.price-int64(i), nil)
			if e != nil {
				return "", e
			}
			ids = append(ids, id)
		}
		snap, e := r.snapshot(r.ctx, o)
		if e != nil {
			return "", e
		}
		return "TCP Logon has COD=true; two resting bid levels", assertScenario(len(snap.BidLevels) == 2, "expected two bid levels")
	}); e != nil {
		return e
	}
	if e := r.step("Drop TCP and observe both cancels on the book", "ladder", func() (string, error) {
		mark := o.mark()
		s.close()
		_, e := o.wait(r.ctx, mark, "both disconnect cancels on the book", func(m *protobuf.FXPMessage) bool {
			qty := int32(0)
			for _, msg := range o.messages[mark:] {
				if inc := msg.GetMarketDataIncremental(); inc != nil {
					for _, ch := range inc.Changes {
						if ch.UpdateType == 2 {
							qty += ch.Quantity
						}
					}
				}
			}
			return qty == 2*r.qty
		})
		if e != nil {
			return "", e
		}
		if e = r.emptyBook(o); e != nil {
			return "", e
		}
		return fmt.Sprintf("TCP dropped; %d units removed; book empty", 2*r.qty), nil
	}); e != nil {
		return e
	}
	return r.step("Reconnect and retrieve both stored canceled reports", "blotter", func() (string, error) {
		recovered, e := r.connect(r.ctx, "cod-user", false)
		if e != nil {
			return "", e
		}
		r.sessions = append(r.sessions, recovered)
		for i, id := range ids {
			request := fmt.Sprintf("stored-%d", i)
			e = recovered.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusRequest{OrderStatusRequest: &protobuf.OrderStatusRequest{RequestId: request, OrderId: id, Symbol: r.Symbol}}})
			if e != nil {
				return "", e
			}
			m, e := recovered.wait(r.ctx, 0, "stored canceled report", func(m *protobuf.FXPMessage) bool {
				return m.GetOrderStatusResponse() != nil && m.GetOrderStatusResponse().RequestId == request && m.GetOrderStatusResponse().Complete
			})
			if e != nil {
				return "", e
			}
			response := m.GetOrderStatusResponse()
			if e = assertScenario(response.RejectReason == "" && len(response.Reports) == 1 && response.Reports[0].OrderStatus == protobuf.OrderStatus_ORDER_CANCELED && response.Reports[0].LeavesQuantity == 0, "expected retained ORDER_CANCELED report with zero leaves for order %d", i+1); e != nil {
				return "", e
			}
		}
		return fmt.Sprintf("TCP dropped; %d units removed; empty book; 2 retained canceled reports with leaves=0", 2*r.qty), nil
	})
}

// Gateway pool readers can deliver private reports out of order. Core X<n>
// execution IDs are monotonic; public trade seq/price independently checks the
// cascade. Never use browser/network arrival timing as engine execution order.
func sortScenarioReports(reports []*protobuf.ExecutionReport) error {
	numbers := map[string]uint64{}
	for _, rep := range reports {
		n, e := strconv.ParseUint(strings.TrimPrefix(rep.ExecutionId, "X"), 10, 64)
		if e != nil || !strings.HasPrefix(rep.ExecutionId, "X") {
			return fmt.Errorf("cannot establish core execution order from ExecID %q", rep.ExecutionId)
		}
		numbers[rep.ExecutionId] = n
	}
	sort.Slice(reports, func(i, j int) bool { return numbers[reports[i].ExecutionId] < numbers[reports[j].ExecutionId] })
	return nil
}
