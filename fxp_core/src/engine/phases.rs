// SPDX-License-Identifier: MIT
use super::*;
use session::Boundary;

pub(super) fn is_call(phase: fxp::TradingPhase) -> bool {
    matches!(
        phase,
        fxp::TradingPhase::OpeningCall | fxp::TradingPhase::ClosingCall
    )
}

pub(super) fn entry_reason(
    phase: fxp::TradingPhase,
    full: bool,
    ty: OrderType,
    tif: TimeInForce,
) -> Option<&'static str> {
    if phase == fxp::TradingPhase::Closed {
        return Some("market closed");
    }
    if matches!(tif, TimeInForce::AtOpen | TimeInForce::AtClose) {
        if !full {
            return Some("no auction scheduled");
        }
        if matches!(ty, OrderType::Stop | OrderType::StopLimit) {
            return Some("auction time in force requires limit or market");
        }
        if tif == TimeInForce::AtOpen && phase != fxp::TradingPhase::OpeningCall {
            return Some("opening auction not in progress");
        }
    }
    if is_call(phase) && matches!(tif, TimeInForce::IOC | TimeInForce::FOK) {
        return Some("IOC/FOK not supported during auction call");
    }
    None
}

pub(super) fn timestamp(now: Time) -> Option<Timestamp> {
    Some(Timestamp {
        seconds: now.seconds,
        nanos: now.nanos,
    })
}

impl<R: Clone, C: Clock, I: ExecIds> Engine<R, C, I> {
    pub(super) fn advance(&mut self) -> Vec<Output<R>> {
        let now = self.clock.now();
        let mut cursor = self.last_seen;
        let mut outputs = Vec::new();
        while cursor < now {
            let scheduled = self.schedule.map(|s| s.next_boundary(cursor.seconds));
            let expiry = self
                .states
                .values()
                .filter(|s| s.time_in_force == TimeInForce::Gtd)
                .filter_map(|s| expiry_instant(&s.expire_date).ok())
                .filter(|seconds| {
                    (Time {
                        seconds: *seconds,
                        nanos: 0,
                    }) > cursor
                })
                .min();
            let Some(seconds) = scheduled.map(|s| s.0).into_iter().chain(expiry).min() else {
                break;
            };
            let at = Time { seconds, nanos: 0 };
            if at > now {
                break;
            }
            let boundary = scheduled.filter(|s| s.0 == seconds).map(|s| s.1);
            let full = self.schedule.is_some_and(SessionSchedule::is_full);
            self.expire(at, !full && boundary == Some(Boundary::Close), &mut outputs);
            if full && let Some(kind) = boundary {
                self.session_boundary(kind, at, &mut outputs);
            }
            self.publish_indicative(at, &mut outputs);
            cursor = at;
        }
        if now > self.last_seen {
            self.last_seen = now;
        }
        outputs
    }

    fn set_phase(&mut self, phase: fxp::TradingPhase, at: Time, out: &mut Vec<Output<R>>) {
        if self.phase == phase {
            return;
        }
        self.phase = phase;
        self.last_indicative = None;
        out.push(Output::TradingSessionStatus(fxp::TradingSessionStatus {
            symbol: self.symbol.clone(),
            phase: phase as i32,
            timestamp: timestamp(at),
        }));
    }

    fn session_boundary(&mut self, boundary: Boundary, at: Time, out: &mut Vec<Output<R>>) {
        match boundary {
            Boundary::OpenCall => self.set_phase(fxp::TradingPhase::OpeningCall, at, out),
            Boundary::Open => {
                let price = self.run_auction(at, out);
                self.cancel_remainders(TimeInForce::AtOpen, at, out);
                self.set_phase(fxp::TradingPhase::Continuous, at, out);
                if let Some(price) = price {
                    let mut exec = Execution::default();
                    for stop in self.stop_book.drain_triggered(price) {
                        exec.triggered.push(stop.order_id.clone());
                        self.book.execute_into(stop, &mut exec);
                    }
                    run_stop_cascade(
                        &mut self.book,
                        &mut self.stop_book,
                        &mut self.last_trade_price,
                        &mut exec,
                    );
                    report_execution(
                        &mut self.ids,
                        out,
                        exec,
                        &mut self.states,
                        &self.symbol,
                        &timestamp(at),
                    );
                }
            }
            Boundary::CloseCall => {
                self.set_phase(fxp::TradingPhase::ClosingCall, at, out);
                let mut held: Vec<_> = self.held_close.drain().map(|(_, o)| o).collect();
                held.sort_by_key(|o| self.states[&o.order_id].arrival);
                for order in held {
                    if order.order_type == OrderType::Market {
                        self.call_markets.insert(order.order_id.clone(), order);
                    } else {
                        let md = self.book.add_order(order);
                        out.push(Output::MarketData(build_md_update(&md, &timestamp(at))));
                    }
                }
            }
            Boundary::Close => {
                self.run_auction(at, out);
                self.cancel_remainders(TimeInForce::AtClose, at, out);
                self.set_phase(fxp::TradingPhase::Closed, at, out);
                self.expire(at, true, out);
            }
        }
    }

    fn auction_orders(&self) -> Vec<auction::AuctionOrder<'_>> {
        self.book
            .order_lookup
            .values()
            .chain(self.call_markets.values())
            .map(|o| auction::AuctionOrder {
                id: &o.order_id,
                side: match o.side {
                    OrderSide::Buy => auction::AuctionSide::Buy,
                    OrderSide::Sell => auction::AuctionSide::Sell,
                },
                limit_price: if o.order_type == OrderType::Market {
                    None
                } else {
                    Some(o.price)
                },
                quantity: o.quantity,
                seq: self
                    .book
                    .arrival
                    .get(&o.order_id)
                    .copied()
                    .unwrap_or(self.states[&o.order_id].arrival),
            })
            .collect()
    }

    pub(super) fn publish_indicative(&mut self, at: Time, out: &mut Vec<Output<R>>) {
        if !is_call(self.phase) {
            return;
        }
        let result = auction::uncross(&self.auction_orders(), self.last_trade_price);
        if self.last_indicative == Some(result) {
            return;
        }
        self.last_indicative = Some(result);
        out.push(Output::AuctionInfo(fxp::AuctionInfo {
            symbol: self.symbol.clone(),
            phase: self.phase as i32,
            indicative_price: result.price,
            indicative_volume: result.volume,
            imbalance_quantity: result.imbalance.unsigned_abs() as u64,
            imbalance_side: if result.imbalance > 0 {
                fxp::OrderSide::Buy as i32
            } else if result.imbalance < 0 {
                fxp::OrderSide::Sell as i32
            } else {
                fxp::OrderSide::Unspecified as i32
            },
            reference_price: self.last_trade_price.unwrap_or(0),
            timestamp: timestamp(at),
        }));
    }

    fn run_auction(&mut self, at: Time, out: &mut Vec<Output<R>>) -> Option<i64> {
        let orders = self.auction_orders();
        let result = auction::uncross(&orders, self.last_trade_price);
        let fills: Vec<_> = auction::allocate(&orders, result)
            .into_iter()
            .map(|f| (f.buy_id.to_string(), f.sell_id.to_string(), f.quantity))
            .collect();
        let mut md = Vec::new();
        for (buy, sell, quantity) in fills {
            for id in [&buy, &sell] {
                if self.book.order_lookup.contains_key(id) {
                    md.push(self.book.apply_auction_fill(id, quantity));
                } else {
                    let order = self.call_markets.get_mut(id).expect("live auction market");
                    order.quantity -= quantity;
                    if order.quantity == 0 {
                        self.call_markets.remove(id);
                    }
                }
                if let Some((mut report, owner)) = apply_fill(
                    &mut self.ids,
                    &mut self.states,
                    id,
                    &self.symbol,
                    result.price,
                    quantity,
                    &timestamp(at),
                ) {
                    report.auction = true;
                    out.push(Output::ExecutionReport(owner, report));
                }
            }
        }
        for update in md {
            out.push(Output::MarketData(build_md_update(&update, &timestamp(at))));
        }
        if result.volume == 0 {
            return None;
        }
        self.last_trade_price = Some(result.price);
        Some(result.price)
    }

    pub(super) fn remove_live(&mut self, id: &str) -> Option<MarketDataIncrementalUpdate> {
        if let Some(md) = self.book.cancel_order(id) {
            return Some(md);
        }
        if self.call_markets.remove(id).is_none() && self.held_close.remove(id).is_none() {
            self.stop_book
                .cancel_for(id, &self.states[id].sender)
                .expect("live parked order");
        }
        None
    }

    fn finish_order(&mut self, id: &str, expired: bool, at: Time, out: &mut Vec<Output<R>>) {
        let md = self.remove_live(id);
        let state = self.states.remove(id).expect("live terminal state");
        let (et, os) = if expired {
            (
                fxp::ExecutionType::ExecExpired,
                fxp::OrderStatus::OrderExpired,
            )
        } else {
            (
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled,
            )
        };
        let ev = ReportEvent {
            orig_order_id: id,
            ..ReportEvent::new(et, os)
        };
        let report = build_report(
            &mut self.ids,
            id,
            &self.symbol,
            Some(&state),
            ev,
            &timestamp(at),
        );
        out.push(Output::ExecutionReport(state.output_tx, report));
        if let Some(md) = md {
            out.push(Output::MarketData(build_md_update(&md, &timestamp(at))));
        }
    }

    fn expire(&mut self, at: Time, day: bool, out: &mut Vec<Output<R>>) {
        let mut due: Vec<_> = self
            .states
            .iter()
            .filter(|(_, s)| {
                (day && s.time_in_force == TimeInForce::Day)
                    || (s.time_in_force == TimeInForce::Gtd
                        && expiry_instant(&s.expire_date).is_ok_and(|t| {
                            (Time {
                                seconds: t,
                                nanos: 0,
                            }) <= at
                        }))
            })
            .map(|(id, s)| (s.arrival, id.clone()))
            .collect();
        due.sort();
        for (_, id) in due {
            self.finish_order(&id, true, at, out);
        }
    }

    fn cancel_remainders(&mut self, tif: TimeInForce, at: Time, out: &mut Vec<Output<R>>) {
        let mut due: Vec<_> = self
            .states
            .iter()
            .filter(|(id, s)| s.time_in_force == tif || self.call_markets.contains_key(*id))
            .map(|(id, s)| (s.arrival, id.clone()))
            .collect();
        due.sort();
        for (_, id) in due {
            self.finish_order(&id, false, at, out);
        }
    }

    pub(super) fn preflight(&mut self, msg: &SymbolMsg<R>, out: &mut Vec<Output<R>>) -> bool {
        match msg {
            SymbolMsg::Cancel(c)
                if self.call_markets.contains_key(&c.order_id)
                    || self.held_close.contains_key(&c.order_id) =>
            {
                let state = &self.states[&c.order_id];
                if state.sender != c.sender {
                    out.push(Output::ExecutionReport(
                        c.output_tx.clone(),
                        make_reject_report(
                            &mut self.ids,
                            &c.order_id,
                            &self.symbol,
                            &c.client_cancel_id,
                            OrderReject::NotOwner.reason(),
                            &timestamp(self.clock.now()),
                        ),
                    ));
                } else {
                    self.remove_live(&c.order_id);
                    let state = self.states.remove(&c.order_id).unwrap();
                    let ev = ReportEvent {
                        client_order_id: Some(&c.client_cancel_id),
                        orig_order_id: &c.order_id,
                        ..ReportEvent::new(
                            fxp::ExecutionType::ExecCanceled,
                            fxp::OrderStatus::OrderCanceled,
                        )
                    };
                    out.push(Output::ExecutionReport(
                        c.output_tx.clone(),
                        build_report(
                            &mut self.ids,
                            &c.order_id,
                            &self.symbol,
                            Some(&state),
                            ev,
                            &timestamp(self.clock.now()),
                        ),
                    ));
                }
                true
            }
            SymbolMsg::CancelReplace(c) => {
                let parsed = parse_replace_time_in_force(c.new_time_in_force);
                let state = self.states.get(&c.order_id);
                let reason = if self.phase == fxp::TradingPhase::Closed {
                    Some("market closed".to_string())
                } else if state.is_none_or(|s| s.sender != c.sender) {
                    Some(OrderReject::UnknownOrder.reason().to_string())
                } else if self.held_close.contains_key(&c.order_id) {
                    Some("replace of held AT_CLOSE order is not supported".to_string())
                } else if c.new_order_id != c.order_id && self.states.contains_key(&c.new_order_id)
                {
                    Some(OrderReject::DuplicateOrderId.reason().to_string())
                } else {
                    match &parsed {
                        Err(e) => Some(e.clone()),
                        Ok(tif) => {
                            let state = state.unwrap();
                            let tif = tif.unwrap_or(state.time_in_force);
                            let ty = if self.call_markets.contains_key(&c.order_id) {
                                OrderType::Market
                            } else if self.stop_book.contains(&c.order_id) {
                                OrderType::Stop
                            } else {
                                OrderType::Limit
                            };
                            entry_reason(
                                self.phase,
                                self.schedule.is_some_and(SessionSchedule::is_full),
                                ty,
                                tif,
                            )
                            .map(str::to_string)
                            .or_else(|| {
                                if tif == TimeInForce::Gtd {
                                    validate_expiry(&state.expire_date, self.clock.now()).err()
                                } else {
                                    None
                                }
                            })
                        }
                    }
                };
                if let Some(reason) = reason {
                    out.push(Output::ExecutionReport(
                        c.output_tx.clone(),
                        make_reject_report(
                            &mut self.ids,
                            &c.order_id,
                            &self.symbol,
                            &c.client_replace_id,
                            &reason,
                            &timestamp(self.clock.now()),
                        ),
                    ));
                    return true;
                }
                if !self.call_markets.contains_key(&c.order_id) {
                    return false;
                }
                let old = &self.call_markets[&c.order_id];
                let state = &self.states[&c.order_id];
                if c.new_quantity > 0 && c.new_quantity as u32 <= state.cum_qty {
                    out.push(Output::ExecutionReport(
                        c.output_tx.clone(),
                        make_reject_report(
                            &mut self.ids,
                            &c.order_id,
                            &self.symbol,
                            &c.client_replace_id,
                            OrderReject::QuantityNotAboveFilled.reason(),
                            &timestamp(self.clock.now()),
                        ),
                    ));
                    return true;
                }
                let quantity = if c.new_quantity > 0 {
                    c.new_quantity as u32 - state.cum_qty
                } else {
                    old.quantity
                };
                let replacement = Order {
                    order_id: c.new_order_id.clone(),
                    quantity,
                    time_in_force: parsed.unwrap().unwrap_or(old.time_in_force),
                    timestamp: self.clock.now().as_millis(),
                    client_order_id: c.client_replace_id.clone(),
                    ..old.clone()
                };
                self.call_markets.remove(&c.order_id);
                let mut state = self.states.remove(&c.order_id).unwrap();
                state.order_qty = state.cum_qty + quantity;
                state.time_in_force = replacement.time_in_force;
                state.client_order_id = c.client_replace_id.clone();
                state.output_tx = c.output_tx.clone();
                state.arrival = self.next_arrival;
                send_replace_reports(
                    &mut self.ids,
                    out,
                    c,
                    &self.symbol,
                    &state,
                    &timestamp(self.clock.now()),
                );
                self.states.insert(c.new_order_id.clone(), state);
                if replacement.time_in_force == TimeInForce::AtClose
                    && self.phase != fxp::TradingPhase::ClosingCall
                {
                    self.held_close.insert(c.new_order_id.clone(), replacement);
                } else {
                    self.call_markets
                        .insert(c.new_order_id.clone(), replacement);
                }
                true
            }
            _ => false,
        }
    }
}
