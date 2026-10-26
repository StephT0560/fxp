// SPDX-License-Identifier: MIT

use crate::fxp;
use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketUpdateType};
use crate::order_book::matcher::{StpAction, StpEvent};
use crate::order_book::order::{Order, OrderSide, OrderType, Stp, StpMode, TimeInForce};
use crate::order_book::order_book::{OrderBook, OrderReject, OrderReplace};
use crate::order_book::stops::{Execution, StopBook, StopReplace, run_stop_cascade, stop_fires};
use prost_types::Timestamp;
use std::collections::{HashMap, VecDeque};
use std::sync::Arc;

pub mod auction;
#[cfg(test)]
mod iceberg_tests;
#[cfg(test)]
mod mass_cancel_tests;
#[cfg(test)]
mod persistence_tests;
#[cfg(test)]
mod phase_tests;
mod phases;
mod session;
#[cfg(test)]
mod status_tests;
#[cfg(test)]
mod stp_tests;
#[cfg(test)]
mod trade_feed_tests;
use session::expiry_instant;
pub use session::{SessionSchedule, UtcTimeOfDay};

struct OrderMsg<R> {
    pub order: fxp::NewOrderSingle,
    pub output_tx: R,
}

pub struct CancelMsg<R> {
    pub order_id: String,
    pub sender: String, // requesting trader; must own the order
    pub client_cancel_id: String,
    pub output_tx: R,
}

pub struct CancelReplaceMsg<R> {
    pub order_id: String,
    pub sender: String, // requesting trader; must own the order
    pub new_order_id: String,
    pub client_replace_id: String,
    pub new_price: i64,    // 0 = no change
    pub new_quantity: i32, // 0 = no change
    pub new_time_in_force: i32,
    pub new_stop_price: i64, // 0 = no change (parked stops only)
    pub new_display_quantity: Option<i32>,
    pub output_tx: R,
}

/// Cancel every live order of `sender` in this symbol (book, parked stops,
/// auction-held orders), optionally only one side.
pub struct MassCancelMsg {
    pub sender: String,
    /// `fxp::OrderSide`; 0 = both sides.
    pub side: i32,
    /// Echoed only on the mass-cancel report; each canceled order's ER keeps
    /// that order's own client_order_id.
    pub client_request_id: String,
    /// `fxp::MassCancelReason`.
    pub reason: i32,
}

enum SymbolMsg<R> {
    Order(OrderMsg<R>),
    Cancel(CancelMsg<R>),
    CancelReplace(CancelReplaceMsg<R>),
}

fn parse_order_enums(
    order: &fxp::NewOrderSingle,
) -> Result<(OrderSide, OrderType, TimeInForce), String> {
    let side = match fxp::OrderSide::try_from(order.side) {
        Ok(fxp::OrderSide::Buy) => OrderSide::Buy,
        Ok(fxp::OrderSide::Sell) => OrderSide::Sell,
        Ok(fxp::OrderSide::Unspecified) => return Err("side is required".into()),
        Err(_) => return Err(format!("unknown side {}", order.side)),
    };
    let order_type = match fxp::OrderType::try_from(order.order_type) {
        Ok(fxp::OrderType::Market) => OrderType::Market,
        Ok(fxp::OrderType::Limit) => OrderType::Limit,
        Ok(fxp::OrderType::Stop) => OrderType::Stop,
        Ok(fxp::OrderType::StopLimit) => OrderType::StopLimit,
        Ok(fxp::OrderType::Unspecified) => return Err("order_type is required".into()),
        Err(_) => return Err(format!("unknown order_type {}", order.order_type)),
    };
    let time_in_force = match fxp::TimeInForce::try_from(order.time_in_force) {
        Ok(fxp::TimeInForce::Day) => TimeInForce::Day,
        Ok(fxp::TimeInForce::Gtc) => TimeInForce::GTC,
        Ok(fxp::TimeInForce::Ioc) => TimeInForce::IOC,
        Ok(fxp::TimeInForce::Fok) => TimeInForce::FOK,
        Ok(fxp::TimeInForce::AtOpen) => TimeInForce::AtOpen,
        Ok(fxp::TimeInForce::AtClose) => TimeInForce::AtClose,
        Ok(fxp::TimeInForce::Gtd) => TimeInForce::Gtd,
        Ok(fxp::TimeInForce::Unspecified) => return Err("time_in_force is required".into()),
        Err(_) => return Err(format!("unknown time_in_force {}", order.time_in_force)),
    };
    Ok((side, order_type, time_in_force))
}

/// Resolve an order's STP instruction: `(mode, explicitly requested)`.
fn parse_stp(value: i32, default: StpMode) -> Result<(StpMode, bool), String> {
    use fxp::SelfTradePrevention as P;
    let explicit = |mode| Ok((mode, true));
    match P::try_from(value) {
        Ok(P::Unspecified) => Ok((default, false)),
        Ok(P::None) => Ok((StpMode::None, false)),
        Ok(P::CancelNewest) => explicit(StpMode::CancelNewest),
        Ok(P::CancelOldest) => explicit(StpMode::CancelOldest),
        Ok(P::CancelBoth) => explicit(StpMode::CancelBoth),
        Ok(P::DecrementAndCancel) => explicit(StpMode::DecrementAndCancel),
        Err(_) => Err(format!("unknown stp_mode {value}")),
    }
}

fn parse_replace_time_in_force(value: i32) -> Result<Option<TimeInForce>, String> {
    match fxp::TimeInForce::try_from(value) {
        Ok(fxp::TimeInForce::Unspecified) => Ok(None),
        Ok(fxp::TimeInForce::Day) => Ok(Some(TimeInForce::Day)),
        Ok(fxp::TimeInForce::Gtc) => Ok(Some(TimeInForce::GTC)),
        Ok(fxp::TimeInForce::Ioc) => Ok(Some(TimeInForce::IOC)),
        Ok(fxp::TimeInForce::Fok) => Ok(Some(TimeInForce::FOK)),
        Ok(fxp::TimeInForce::AtOpen) => Ok(Some(TimeInForce::AtOpen)),
        Ok(fxp::TimeInForce::AtClose) => Ok(Some(TimeInForce::AtClose)),
        Ok(fxp::TimeInForce::Gtd) => Ok(Some(TimeInForce::Gtd)),
        Err(_) => Err(format!("unknown new_time_in_force {}", value)),
    }
}

pub trait Clock {
    fn now(&self) -> Time;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Time {
    pub seconds: i64,
    pub nanos: i32,
}

impl Time {
    fn as_millis(self) -> u64 {
        self.seconds as u64 * 1000 + self.nanos as u64 / 1_000_000
    }
}

pub trait ExecIds {
    fn next_id(&mut self) -> String;
}

pub enum Output<R> {
    TradingSessionStatus(fxp::TradingSessionStatus),
    AuctionInfo(fxp::AuctionInfo),
    ExecutionReport(R, fxp::ExecutionReport),
    MarketData(fxp::MarketDataIncrementalUpdate),
    Trade(fxp::TradeCaptureReport),
    Snapshot(R, fxp::MarketDataSnapshot),
    /// Number of orders a mass cancel canceled; follows their reports.
    MassCancelCount(u32),
}

pub struct Engine<R, C, I> {
    symbol: String,
    book: OrderBook,
    stop_book: StopBook,
    last_trade_price: Option<i64>,
    trade_seq: u64,
    states: HashMap<String, OrderState<R>>,
    clock: C,
    ids: I,
    schedule: Option<SessionSchedule>,
    /// Operator-started opening call: uncross at this instant.
    call_until: Option<Time>,
    last_seen: Time,
    next_arrival: u64,
    phase: fxp::TradingPhase,
    call_markets: HashMap<String, Order>,
    held_close: HashMap<String, Order>,
    last_indicative: Option<auction::AuctionResult>,
    stp_default: StpMode,
    symbol_rules: crate::symbol_config::SymbolRules,
    status_history: HashMap<String, (String, fxp::ExecutionReport)>,
    terminal_history: VecDeque<(String, String)>,
}

impl<R: Clone, C: Clock, I: ExecIds> Engine<R, C, I> {
    pub fn phase(&self) -> fxp::TradingPhase {
        self.phase
    }
    pub fn new(symbol: &str, clock: C, ids: I, schedule: Option<SessionSchedule>) -> Self {
        let last_seen = clock.now();
        let phase = schedule.map_or(fxp::TradingPhase::Continuous, |s| {
            s.phase(last_seen.seconds)
        });
        Self {
            symbol: symbol.into(),
            book: OrderBook::new(symbol, Arc::new(MarketDataBroadcaster::new())),
            stop_book: StopBook::default(),
            last_trade_price: None,
            trade_seq: 0,
            states: HashMap::new(),
            clock,
            ids,
            schedule,
            call_until: None,
            last_seen,
            next_arrival: 0,
            phase,
            call_markets: HashMap::new(),
            held_close: HashMap::new(),
            last_indicative: Some(auction::AuctionResult::default()),
            stp_default: StpMode::None,
            symbol_rules: Default::default(),
            status_history: HashMap::new(),
            terminal_history: VecDeque::new(),
        }
    }
    /// Self-trade prevention applied to orders with `STP_UNSPECIFIED`.
    pub fn with_stp_default(mut self, mode: StpMode) -> Self {
        self.stp_default = mode;
        self
    }
    pub fn with_symbol_rules(mut self, rules: crate::symbol_config::SymbolRules) -> Self {
        self.symbol_rules = rules;
        self
    }
    pub fn new_order(&mut self, order: fxp::NewOrderSingle, owner: R) -> Vec<Output<R>> {
        let id = order.order_id.clone();
        let sender = order.sender.clone();
        let outputs = self.process(SymbolMsg::Order(OrderMsg {
            order,
            output_tx: owner,
        }));
        self.bind_status_owner(&id, sender, &outputs);
        self.remember_status(&outputs);
        outputs
    }
    pub fn cancel(&mut self, request: CancelMsg<R>) -> Vec<Output<R>> {
        let outputs = self.process(SymbolMsg::Cancel(request));
        self.remember_status(&outputs);
        outputs
    }
    pub fn cancel_replace(&mut self, request: CancelReplaceMsg<R>) -> Vec<Output<R>> {
        let id = request.new_order_id.clone();
        let sender = request.sender.clone();
        let outputs = self.process(SymbolMsg::CancelReplace(request));
        self.bind_status_owner(&id, sender, &outputs);
        self.remember_status(&outputs);
        outputs
    }
    fn bind_status_owner(&mut self, id: &str, sender: String, outputs: &[Output<R>]) {
        let accepted = outputs
            .iter()
            .rev()
            .find_map(|o| match o {
                Output::ExecutionReport(_, r) if r.order_id == id => {
                    Some(r.execution_type != fxp::ExecutionType::ExecRejected as i32)
                }
                _ => None,
            })
            .unwrap_or(false);
        if accepted {
            self.status_history
                .entry(id.into())
                .or_insert_with(|| (String::new(), fxp::ExecutionReport::default()))
                .0 = sender;
        }
    }
    /// Current order snapshots, including a bounded history of 100,000 terminal
    /// transitions per symbol. Never change live routing or expose other owners.
    pub fn order_status(&self, sender: &str, id: &str, side: i32) -> Vec<fxp::ExecutionReport> {
        if sender.is_empty() {
            return Vec::new();
        }
        let mut reports: Vec<_> = self
            .status_history
            .iter()
            .filter(|(key, (owner, r))| {
                owner == sender
                    && (id.is_empty() || key.as_str() == id)
                    && !r.order_id.is_empty()
                    && (side == 0 || side == r.side)
            })
            .map(|(_, (_, r))| fxp::ExecutionReport {
                execution_type: fxp::ExecutionType::ExecOrderStatus as i32,
                execution_id: "0".into(),
                executed_quantity: 0,
                execution_price: 0,
                ..r.clone()
            })
            .collect();
        reports.sort_by(|a, b| a.order_id.cmp(&b.order_id));
        reports
    }
    fn remember_status(&mut self, outputs: &[Output<R>]) {
        for output in outputs {
            let Output::ExecutionReport(_, report) = output else {
                continue;
            };
            if report.execution_type == fxp::ExecutionType::ExecRejected as i32 {
                continue;
            }
            if let Some((_, current)) = self.status_history.get_mut(&report.order_id) {
                if current.execution_id == report.execution_id {
                    continue;
                }
                *current = report.clone();
                if report.leaves_quantity == 0 {
                    self.terminal_history
                        .push_back((report.order_id.clone(), report.execution_id.clone()));
                }
            }
            if !report.orig_order_id.is_empty()
                && report.execution_type == fxp::ExecutionType::ExecReplaced as i32
                && let Some((_, old)) = self.status_history.get_mut(&report.orig_order_id)
            {
                old.order_status = fxp::OrderStatus::OrderReplaced as i32;
                old.leaves_quantity = 0;
                self.terminal_history
                    .push_back((report.orig_order_id.clone(), old.execution_id.clone()));
            }
        }
        while self.terminal_history.len() > 100_000 {
            let (id, exec) = self.terminal_history.pop_front().expect("terminal history");
            if self
                .status_history
                .get(&id)
                .is_some_and(|(_, r)| r.execution_id == exec && r.leaves_quantity == 0)
            {
                self.status_history.remove(&id);
            }
        }
    }

    pub fn mass_cancel(&mut self, request: MassCancelMsg) -> Vec<Output<R>> {
        let mut outputs = self.tick();
        let ts = phases::timestamp(self.clock.now());
        let mut targets: Vec<(u64, String)> = self
            .states
            .iter()
            .filter(|(_, s)| {
                s.sender == request.sender && (request.side == 0 || s.side == request.side)
            })
            .map(|(id, s)| (s.arrival, id.clone()))
            .collect();
        targets.sort();
        let reason = match fxp::MassCancelReason::try_from(request.reason) {
            Ok(fxp::MassCancelReason::Disconnect) => "cancel on disconnect",
            _ => "mass cancel",
        };
        for (_, id) in &targets {
            let md = self.remove_live(id);
            let state = self.states.remove(id).expect("live order state");
            let ev = ReportEvent {
                reject_reason: reason,
                ..ReportEvent::new(
                    fxp::ExecutionType::ExecCanceled,
                    fxp::OrderStatus::OrderCanceled,
                )
            };
            outputs.push(Output::ExecutionReport(
                state.output_tx.clone(),
                build_report(&mut self.ids, id, &self.symbol, Some(&state), ev, &ts),
            ));
            if let Some(md) = md {
                outputs.push(Output::MarketData(build_md_update(&md, &ts)));
            }
        }
        outputs.push(Output::MassCancelCount(targets.len() as u32));
        self.publish_indicative(self.clock.now(), &mut outputs);
        self.remember_status(&outputs);
        outputs
    }
    /// Snapshot for the new subscriber, then the current phase and (in a call)
    /// the indicative: both are otherwise only published on change.
    pub fn subscribe(&mut self, owner: R) -> Vec<Output<R>> {
        let mut outputs = self.tick();
        let now = self.clock.now();
        outputs.push(Output::Snapshot(owner, snapshot(&self.book, now)));
        outputs.push(Output::TradingSessionStatus(fxp::TradingSessionStatus {
            symbol: self.symbol.clone(),
            phase: self.phase as i32,
            timestamp: Some(Timestamp {
                seconds: now.seconds,
                nanos: now.nanos,
            }),
        }));
        // Re-publish the current indicative so a late joiner sees the call.
        self.last_indicative = None;
        self.publish_indicative(now, &mut outputs);
        outputs
    }
    /// Operator override: start an opening call now and uncross after
    /// `seconds`. Ignored while a call is already running.
    pub fn begin_opening_call(&mut self, seconds: u32) -> Vec<Output<R>> {
        let mut outputs = self.tick();
        if seconds == 0 || phases::is_call(self.phase) {
            return outputs;
        }
        let now = self.clock.now();
        self.call_until = Some(Time {
            seconds: now.seconds + i64::from(seconds),
            nanos: 0,
        });
        self.set_phase(fxp::TradingPhase::OpeningCall, now, &mut outputs);
        self.publish_indicative(now, &mut outputs);
        self.remember_status(&outputs);
        outputs
    }
    fn auctions_enabled(&self) -> bool {
        self.call_until.is_some() || self.schedule.is_some_and(SessionSchedule::is_full)
    }
    pub fn tick(&mut self) -> Vec<Output<R>> {
        let outputs = self.advance();
        self.remember_status(&outputs);
        outputs
    }
    fn process(&mut self, msg: SymbolMsg<R>) -> Vec<Output<R>> {
        let mut outputs = self.tick();
        self.next_arrival += 1;
        let arrival = self.next_arrival;
        if self.preflight(&msg, &mut outputs) {
            self.publish_indicative(self.clock.now(), &mut outputs);
            return outputs;
        }
        let symbol_rules = self.symbol_rules;
        let phase = self.phase;
        let full_schedule = self.auctions_enabled();
        let call_markets = &mut self.call_markets;
        let held_close = &mut self.held_close;
        let symbol = &self.symbol;
        let book = &mut self.book;
        let stop_book = &mut self.stop_book;
        let last_trade_price = &mut self.last_trade_price;
        let states = &mut self.states;
        let ids = &mut self.ids;
        let trade_seq = &mut self.trade_seq;
        'request: {
            match msg {
                SymbolMsg::Cancel(cancel_msg) => {
                    let now = self.clock.now();
                    let ts = Some(Timestamp {
                        seconds: now.seconds,
                        nanos: now.nanos,
                    });

                    let cancelled =
                        match book.cancel_order_for(&cancel_msg.order_id, &cancel_msg.sender) {
                            Err(OrderReject::UnknownOrder) => stop_book
                                .cancel_for(&cancel_msg.order_id, &cancel_msg.sender)
                                .map(|_| None),
                            other => other.map(Some),
                        };
                    match cancelled {
                        Ok(md_update) => {
                            // Order found and cancelled — send EXEC_CANCELED report
                            let state = states.remove(&cancel_msg.order_id);
                            let ev = ReportEvent {
                                client_order_id: Some(&cancel_msg.client_cancel_id),
                                orig_order_id: &cancel_msg.order_id,
                                ..ReportEvent::new(
                                    fxp::ExecutionType::ExecCanceled,
                                    fxp::OrderStatus::OrderCanceled,
                                )
                            };
                            let report = build_report(
                                ids,
                                &cancel_msg.order_id,
                                symbol,
                                state.as_ref(),
                                ev,
                                &ts,
                            );
                            outputs.push(Output::ExecutionReport(
                                cancel_msg.output_tx.clone(),
                                report,
                            ));

                            // Parked stops are not on the book: no market data to broadcast
                            if let Some(md_update) = md_update {
                                outputs.push(Output::MarketData(build_md_update(&md_update, &ts)));
                            }
                        }
                        Err(reject) => {
                            // Not live (filled, cancelled, unknown) or not owned by the requester
                            let report = make_reject_report(
                                ids,
                                &cancel_msg.order_id,
                                symbol,
                                &cancel_msg.client_cancel_id,
                                reject.reason(),
                                &ts,
                            );
                            outputs.push(Output::ExecutionReport(
                                cancel_msg.output_tx.clone(),
                                report,
                            ));
                        }
                    }
                }

                SymbolMsg::CancelReplace(cr_msg) => {
                    let now = self.clock.now();
                    let ts = Some(Timestamp {
                        seconds: now.seconds,
                        nanos: now.nanos,
                    });

                    let new_time_in_force =
                        match parse_replace_time_in_force(cr_msg.new_time_in_force) {
                            Ok(value) => value,
                            Err(reason) => {
                                let report = make_reject_report(
                                    ids,
                                    &cr_msg.order_id,
                                    symbol,
                                    &cr_msg.client_replace_id,
                                    &reason,
                                    &ts,
                                );
                                outputs.push(Output::ExecutionReport(
                                    cr_msg.output_tx.clone(),
                                    report,
                                ));

                                break 'request;
                            }
                        };

                    if new_time_in_force == Some(TimeInForce::Gtd)
                        && let Some(state) = states.get(&cr_msg.order_id)
                        && state.sender == cr_msg.sender
                        && let Err(reason) = validate_expiry(&state.expire_date, now)
                    {
                        let report = make_reject_report(
                            ids,
                            &cr_msg.order_id,
                            symbol,
                            &cr_msg.client_replace_id,
                            &reason,
                            &ts,
                        );
                        outputs.push(Output::ExecutionReport(cr_msg.output_tx, report));
                        break 'request;
                    }

                    if stop_book.contains(&cr_msg.order_id) {
                        let id_in_use = book.order_lookup.contains_key(&cr_msg.new_order_id);
                        let changes = StopReplace {
                            new_price: cr_msg.new_price,
                            new_stop_price: cr_msg.new_stop_price,
                            new_quantity: cr_msg.new_quantity.max(0) as u32,
                            new_time_in_force,
                            client_order_id: &cr_msg.client_replace_id,
                            timestamp: now.as_millis(),
                        };
                        match stop_book.replace_for(
                            &cr_msg.order_id,
                            &cr_msg.new_order_id,
                            changes,
                            &cr_msg.sender,
                            id_in_use,
                        ) {
                            Err(reject) => {
                                let report = make_reject_report(
                                    ids,
                                    &cr_msg.order_id,
                                    symbol,
                                    &cr_msg.client_replace_id,
                                    reject.reason(),
                                    &ts,
                                );
                                outputs.push(Output::ExecutionReport(
                                    cr_msg.output_tx.clone(),
                                    report,
                                ));
                            }
                            Ok(replacement) => {
                                states.remove(&cr_msg.order_id);
                                let new_state = OrderState {
                                    sender: replacement.trader_id.clone(),
                                    time_in_force: replacement.time_in_force,
                                    expire_date: replacement.expire_date.clone(),
                                    arrival,
                                    side: side_to_proto(replacement.side),
                                    order_qty: replacement.quantity,
                                    display_quantity: replacement.display_quantity,
                                    cum_qty: 0,
                                    notional: 0,
                                    account: replacement.account.clone(),
                                    client_order_id: cr_msg.client_replace_id.clone(),
                                    output_tx: cr_msg.output_tx.clone(),
                                };
                                send_replace_reports(
                                    ids,
                                    &mut outputs,
                                    &cr_msg,
                                    symbol,
                                    &new_state,
                                    &ts,
                                );
                                states.insert(cr_msg.new_order_id.clone(), new_state);
                                // A new stop price may already be through the last print
                                if phase == fxp::TradingPhase::Continuous
                                    && last_trade_price.is_some_and(|p| stop_fires(&replacement, p))
                                {
                                    let order = stop_book
                                        .cancel_for(&replacement.order_id, &replacement.trader_id)
                                        .expect("replacement was just parked");
                                    let mut exec = Execution::default();
                                    exec.triggered.push(order.order_id.clone());
                                    book.execute_into(order, &mut exec);
                                    run_stop_cascade(book, stop_book, last_trade_price, &mut exec);
                                    report_execution(
                                        ids,
                                        &mut outputs,
                                        exec,
                                        states,
                                        symbol,
                                        &ts,
                                        trade_seq,
                                    );
                                }
                            }
                        }
                        break 'request;
                    }

                    let original = book.order_lookup.get(&cr_msg.order_id).cloned();
                    let original_visible = original.as_ref().map_or(0, |o| book.visible(o));
                    let result = book.replace_order(
                        &cr_msg.order_id,
                        &cr_msg.new_order_id,
                        OrderReplace {
                            new_display_quantity: cr_msg.new_display_quantity.map(|q| q as u32),
                            new_price: cr_msg.new_price,
                            new_total_qty: cr_msg.new_quantity.max(0) as u32,
                            new_time_in_force,
                            client_order_id: &cr_msg.client_replace_id,
                            timestamp: now.as_millis(),
                        },
                        &cr_msg.sender,
                    );

                    match result {
                        Err(reject) => {
                            let report = make_reject_report(
                                ids,
                                &cr_msg.order_id,
                                symbol,
                                &cr_msg.client_replace_id,
                                reject.reason(),
                                &ts,
                            );
                            outputs.push(Output::ExecutionReport(cr_msg.output_tx.clone(), report));
                        }
                        Ok((replacement, inc)) => {
                            let (cum_qty, notional) = states
                                .remove(&cr_msg.order_id)
                                .map_or((0, 0), |s| (s.cum_qty, s.notional));
                            let new_state = OrderState {
                                sender: replacement.trader_id.clone(),
                                time_in_force: replacement.time_in_force,
                                expire_date: replacement.expire_date.clone(),
                                arrival,
                                side: side_to_proto(replacement.side),
                                order_qty: cum_qty + replacement.quantity,
                                display_quantity: replacement.display_quantity,
                                cum_qty,
                                notional,
                                account: replacement.account.clone(),
                                client_order_id: cr_msg.client_replace_id.clone(),
                                output_tx: cr_msg.output_tx.clone(),
                            };

                            send_replace_reports(
                                ids,
                                &mut outputs,
                                &cr_msg,
                                symbol,
                                &new_state,
                                &ts,
                            );
                            states.insert(cr_msg.new_order_id.clone(), new_state);

                            if replacement.time_in_force == TimeInForce::AtClose
                                && phase != fxp::TradingPhase::ClosingCall
                            {
                                book.cancel_order(&replacement.order_id);
                                held_close.insert(replacement.order_id.clone(), replacement);
                                let orig = original.expect("successful replacement");
                                let md = book.generate_incremental_update(
                                    MarketUpdateType::CancelOrder,
                                    orig.price,
                                    original_visible,
                                    orig.side,
                                );
                                outputs.push(Output::MarketData(build_md_update(&md, &ts)));
                                break 'request;
                            }
                            if phase != fxp::TradingPhase::Continuous {
                                outputs.push(Output::MarketData(build_md_update(&inc, &ts)));
                                break 'request;
                            }

                            // Immediate fills from the replacement can trigger parked stops
                            let mut exec = Execution::default();
                            book.match_into(&mut exec);
                            run_stop_cascade(book, stop_book, last_trade_price, &mut exec);
                            outputs.push(Output::MarketData(build_md_update(&inc, &ts)));
                            report_execution(
                                ids,
                                &mut outputs,
                                exec,
                                states,
                                symbol,
                                &ts,
                                trade_seq,
                            );
                        }
                    }
                }

                SymbolMsg::Order(order_msg) => {
                    let order = order_msg.order;
                    let output_tx = order_msg.output_tx;
                    let now = self.clock.now();

                    let parsed_enums = parse_order_enums(&order);
                    let stp = parse_stp(order.stp_mode, self.stp_default);
                    let is_limit = match &parsed_enums {
                        Ok((_, order_type, _)) => {
                            matches!(order_type, OrderType::Limit | OrderType::StopLimit)
                        }
                        Err(_) => false,
                    };
                    let invalid = match &parsed_enums {
                        Err(reason) => Some(reason.clone()),
                        Ok(_) if order.quantity <= 0 => {
                            Some("quantity must be positive".to_string())
                        }
                        Ok(_) if is_limit && order.price <= 0 => {
                            Some("limit price must be positive".to_string())
                        }
                        Ok(_) if states.contains_key(&order.order_id) => {
                            Some("Duplicate order id".into())
                        }
                        Ok(_) if stp.is_err() => stp.clone().err(),
                        Ok((_, _, TimeInForce::AtOpen | TimeInForce::AtClose))
                            if stp.as_ref().is_ok_and(|&(mode, explicit)| {
                                explicit && mode != StpMode::None
                            }) =>
                        {
                            Some("self-trade prevention is not supported for auction orders".into())
                        }
                        Ok((_, ty, tif))
                            if phases::entry_reason(phase, full_schedule, *ty, *tif).is_some() =>
                        {
                            phases::entry_reason(phase, full_schedule, *ty, *tif)
                                .map(str::to_string)
                        }
                        Ok((_, _, TimeInForce::Gtd)) => {
                            validate_expiry(&order.expire_date, now).err()
                        }
                        _ => None,
                    };
                    let invalid = invalid.or_else(|| {
                        symbol_rules
                            .validate_quantity(order.quantity as u32)
                            .err()
                            .or_else(|| {
                                if is_limit {
                                    symbol_rules.validate_price(order.price).err()
                                } else {
                                    None
                                }
                            })
                            .or_else(|| {
                                if order.stop_price != 0
                                    && parsed_enums.as_ref().is_ok_and(|(_, ty, _)| {
                                        matches!(ty, OrderType::Stop | OrderType::StopLimit)
                                    })
                                {
                                    symbol_rules.validate_price(order.stop_price).err()
                                } else {
                                    None
                                }
                            })
                    });
                    let invalid = invalid.or_else(|| {
                        let (_, ty, tif) = parsed_enums.as_ref().ok()?;
                        iceberg_reason(
                            order.display_quantity,
                            order.quantity as u32,
                            *ty,
                            *tif,
                            full_schedule,
                            symbol_rules.lot_size,
                        )
                    });
                    if let Some(reason) = invalid {
                        let ts = Some(Timestamp {
                            seconds: now.seconds,
                            nanos: now.nanos,
                        });
                        let state = OrderState::new(
                            &order,
                            order.quantity.max(0) as u32,
                            output_tx.clone(),
                            arrival,
                        );
                        let ev = ReportEvent {
                            reject_reason: &reason,
                            ..ReportEvent::new(
                                fxp::ExecutionType::ExecRejected,
                                fxp::OrderStatus::OrderRejected,
                            )
                        };
                        outputs.push(Output::ExecutionReport(
                            output_tx.clone(),
                            build_report(ids, &order.order_id, symbol, Some(&state), ev, &ts),
                        ));

                        break 'request;
                    }

                    let (side, order_type, time_in_force) =
                        parsed_enums.expect("enum values validated");
                    let order_obj = Order {
                        order_id: order.order_id.clone(),
                        trader_id: order.sender.clone(),
                        symbol: symbol.to_string(),
                        price: order.price,
                        quantity: order.quantity as u32,
                        side,
                        order_type,
                        time_in_force,
                        timestamp: now.as_millis(),
                        stop_price: order.stop_price,
                        expire_date: order.expire_date.clone(),
                        account: order.account.clone(),
                        client_order_id: order.client_order_id.clone(),
                        display_quantity: order.display_quantity as u32,
                        stp: Stp {
                            mode: stp.expect("stp_mode validated").0,
                            group: order.stp_group.clone(),
                        },
                    };

                    let state =
                        OrderState::new(&order, order_obj.quantity, output_tx.clone(), arrival);
                    let ts = Some(Timestamp {
                        seconds: now.seconds,
                        nanos: now.nanos,
                    });

                    // Stop and StopLimit orders rest in stop_book until triggered.
                    // Check immediately in case last_trade_price already satisfies
                    // the stop condition (e.g. order placed after a large move).
                    if matches!(order_obj.order_type, OrderType::Stop | OrderType::StopLimit) {
                        if order_obj.stop_price == 0 {
                            // Reject — stop_price is required
                            let ev = ReportEvent {
                                reject_reason: "stop_price is required",
                                ..ReportEvent::new(
                                    fxp::ExecutionType::ExecRejected,
                                    fxp::OrderStatus::OrderRejected,
                                )
                            };
                            let report = build_report(
                                ids,
                                &order_obj.order_id,
                                symbol,
                                Some(&state),
                                ev,
                                &ts,
                            );
                            outputs.push(Output::ExecutionReport(output_tx.clone(), report));

                            break 'request;
                        }

                        let fires_immediately = phase == fxp::TradingPhase::Continuous
                            && last_trade_price.is_some_and(|p| stop_fires(&order_obj, p));

                        if !fires_immediately {
                            // Park in stop_book — send EXEC_NEW acknowledgement
                            let ev = ReportEvent {
                                leaves: state.leaves(),
                                ..ReportEvent::new(
                                    fxp::ExecutionType::ExecNew,
                                    fxp::OrderStatus::OrderNew,
                                )
                            };
                            let ack = build_report(
                                ids,
                                &order_obj.order_id,
                                symbol,
                                Some(&state),
                                ev,
                                &ts,
                            );
                            outputs.push(Output::ExecutionReport(output_tx.clone(), ack));

                            states.insert(order_obj.order_id.clone(), state);
                            stop_book.park(order_obj);
                            break 'request;
                        }
                    }

                    if phase != fxp::TradingPhase::Continuous
                        || time_in_force == TimeInForce::AtClose
                    {
                        let ev = ReportEvent {
                            leaves: state.leaves(),
                            ..ReportEvent::new(
                                fxp::ExecutionType::ExecNew,
                                fxp::OrderStatus::OrderNew,
                            )
                        };
                        outputs.push(Output::ExecutionReport(
                            output_tx,
                            build_report(ids, &order_obj.order_id, symbol, Some(&state), ev, &ts),
                        ));
                        states.insert(order_obj.order_id.clone(), state);
                        if time_in_force == TimeInForce::AtClose
                            && phase != fxp::TradingPhase::ClosingCall
                        {
                            held_close.insert(order_obj.order_id.clone(), order_obj);
                        } else if order_type == OrderType::Market {
                            call_markets.insert(order_obj.order_id.clone(), order_obj);
                        } else {
                            let md = book.add_order(order_obj);
                            outputs.push(Output::MarketData(build_md_update(&md, &ts)));
                        }
                        break 'request;
                    }

                    // Process the order (non-stop, or stop that triggered immediately)
                    states.insert(order_obj.order_id.clone(), state);
                    let mut exec = Execution::default();
                    if book.execute_into(order_obj.clone(), &mut exec)
                        && let Some(st) = states.get(&order_obj.order_id)
                    {
                        let ev = ReportEvent {
                            leaves: st.leaves(),
                            ..ReportEvent::new(
                                fxp::ExecutionType::ExecNew,
                                fxp::OrderStatus::OrderNew,
                            )
                        };
                        outputs.push(Output::ExecutionReport(
                            output_tx.clone(),
                            build_report(ids, &order_obj.order_id, symbol, Some(st), ev, &ts),
                        ));
                    }
                    run_stop_cascade(book, stop_book, last_trade_price, &mut exec);
                    report_execution(ids, &mut outputs, exec, states, symbol, &ts, trade_seq);
                }
            }
        }
        self.publish_indicative(self.clock.now(), &mut outputs);
        outputs
    }
}

fn validate_expiry(date: &str, now: Time) -> Result<(), String> {
    let seconds = expiry_instant(date)?;
    if (Time { seconds, nanos: 0 }) <= now {
        return Err("expire_date is in the past".into());
    }
    Ok(())
}

fn side_to_proto(side: OrderSide) -> i32 {
    match side {
        OrderSide::Buy => fxp::OrderSide::Buy as i32,
        OrderSide::Sell => fxp::OrderSide::Sell as i32,
    }
}

/// Report state of a live order: original quantity, running fills and the
/// connection of its owner.
struct OrderState<R> {
    sender: String,
    time_in_force: TimeInForce,
    expire_date: String,
    arrival: u64,
    side: i32,
    order_qty: u32,
    display_quantity: u32,
    cum_qty: u32,
    notional: i128,
    account: String,
    client_order_id: String,
    output_tx: R,
}

impl<R: Clone> OrderState<R> {
    fn new(order: &fxp::NewOrderSingle, order_qty: u32, output_tx: R, arrival: u64) -> Self {
        Self {
            sender: order.sender.clone(),
            time_in_force: parse_order_enums(order).map_or(TimeInForce::GTC, |(_, _, tif)| tif),
            expire_date: order.expire_date.clone(),
            arrival,
            side: order.side,
            order_qty,
            display_quantity: order.display_quantity.max(0) as u32,
            cum_qty: 0,
            notional: 0,
            account: order.account.clone(),
            client_order_id: order.client_order_id.clone(),
            output_tx,
        }
    }

    fn leaves(&self) -> u32 {
        self.order_qty.saturating_sub(self.cum_qty)
    }

    fn avg_px(&self) -> i64 {
        if self.cum_qty == 0 {
            0
        } else {
            (self.notional / self.cum_qty as i128) as i64
        }
    }
}

/// Per-event report fields; order-level fields come from `OrderState`.
struct ReportEvent<'a> {
    exec_type: fxp::ExecutionType,
    status: fxp::OrderStatus,
    last_px: i64,
    last_qty: u32,
    leaves: u32,
    /// Request id (cancel/replace) overriding the order's client_order_id.
    client_order_id: Option<&'a str>,
    orig_order_id: &'a str,
    reject_reason: &'a str,
}

impl ReportEvent<'_> {
    fn new(exec_type: fxp::ExecutionType, status: fxp::OrderStatus) -> Self {
        Self {
            exec_type,
            status,
            last_px: 0,
            last_qty: 0,
            leaves: 0,
            client_order_id: None,
            orig_order_id: "",
            reject_reason: "",
        }
    }
}

fn build_report<R: Clone>(
    ids: &mut impl ExecIds,
    order_id: &str,
    symbol: &str,
    state: Option<&OrderState<R>>,
    ev: ReportEvent,
    ts: &Option<Timestamp>,
) -> fxp::ExecutionReport {
    let client_order_id = ev
        .client_order_id
        .map(str::to_string)
        .or_else(|| state.map(|s| s.client_order_id.clone()))
        .unwrap_or_default();
    fxp::ExecutionReport {
        header: Some(fxp::FxpHeader {
            protocol_version: crate::PROTOCOL_VERSION.to_string(),
            sequence_number: 0,
        }),
        execution_id: ids.next_id(),
        order_id: order_id.to_string(),
        symbol: symbol.to_string(),
        execution_type: ev.exec_type.into(),
        order_status: ev.status.into(),
        execution_price: ev.last_px,
        executed_quantity: ev.last_qty as i32,
        timestamp: *ts,
        leaves_quantity: ev.leaves as i32,
        avg_px: state.map_or(0, |s| s.avg_px()),
        cum_quantity: state.map_or(0, |s| s.cum_qty as i32),
        account: state.map_or_else(String::new, |s| s.account.clone()),
        client_order_id,
        orig_order_id: ev.orig_order_id.to_string(),
        reject_reason: ev.reject_reason.to_string(),
        side: state.map_or(0, |s| s.side),
        order_quantity: state.map_or(0, |s| s.order_qty as i32),
        auction: false,
        display_quantity: state.map_or(0, |s| s.display_quantity as i32),
    }
}

/// Apply a fill to a live order and build its report, addressed to the
/// order owner's connection. Fully-filled orders leave `states`.
fn apply_fill<R: Clone>(
    ids: &mut impl ExecIds,
    states: &mut HashMap<String, OrderState<R>>,
    order_id: &str,
    symbol: &str,
    px: i64,
    qty: u32,
    ts: &Option<Timestamp>,
) -> Option<(fxp::ExecutionReport, R)> {
    let st = states.get_mut(order_id)?;
    st.cum_qty += qty;
    st.notional += px as i128 * qty as i128;
    let leaves = st.leaves();
    let status = if leaves == 0 {
        fxp::OrderStatus::OrderFilled
    } else {
        fxp::OrderStatus::OrderPartiallyFilled
    };
    let ev = ReportEvent {
        last_px: px,
        last_qty: qty,
        leaves,
        ..ReportEvent::new(fxp::ExecutionType::ExecFilled, status)
    };
    let frame = build_report(ids, order_id, symbol, Some(&*st), ev, ts);
    let tx = st.output_tx.clone();
    if leaves == 0 {
        states.remove(order_id);
    }
    Some((frame, tx))
}

/// Execution report rejecting a cancel or cancel/replace request. orig_order_id
/// marks it as a request reject: the target order is unchanged.
fn make_reject_report(
    ids: &mut impl ExecIds,
    order_id: &str,
    symbol: &str,
    request_id: &str,
    reason: &str,
    ts: &Option<Timestamp>,
) -> fxp::ExecutionReport {
    let ev = ReportEvent {
        client_order_id: Some(request_id),
        orig_order_id: order_id,
        reject_reason: reason,
        ..ReportEvent::new(
            fxp::ExecutionType::ExecRejected,
            fxp::OrderStatus::OrderRejected,
        )
    };
    build_report::<()>(ids, order_id, symbol, None, ev, ts)
}

fn md_side(side: &OrderSide) -> i32 {
    match side {
        OrderSide::Buy => fxp::OrderSide::Buy as i32,
        OrderSide::Sell => fxp::OrderSide::Sell as i32,
    }
}

/// EXEC_REPLACED for the original order, EXEC_NEW for the replacement.
fn send_replace_reports<R: Clone>(
    ids: &mut impl ExecIds,
    outputs: &mut Vec<Output<R>>,
    cr_msg: &CancelReplaceMsg<R>,
    symbol: &str,
    new_state: &OrderState<R>,
    ts: &Option<Timestamp>,
) {
    let leaves = new_state.leaves();
    for (id, et, os) in [
        (
            &cr_msg.order_id,
            fxp::ExecutionType::ExecReplaced,
            fxp::OrderStatus::OrderReplaced,
        ),
        (
            &cr_msg.new_order_id,
            fxp::ExecutionType::ExecNew,
            fxp::OrderStatus::OrderNew,
        ),
    ] {
        let ev = ReportEvent {
            leaves,
            orig_order_id: &cr_msg.order_id,
            ..ReportEvent::new(et, os)
        };
        outputs.push(Output::ExecutionReport(
            cr_msg.output_tx.clone(),
            build_report(ids, id, symbol, Some(new_state), ev, ts),
        ));
    }
}

fn build_md_update(
    inc: &MarketDataIncrementalUpdate,
    ts: &Option<Timestamp>,
) -> fxp::MarketDataIncrementalUpdate {
    fxp::MarketDataIncrementalUpdate {
        symbol: inc.symbol.clone(),
        header: Some(fxp::FxpHeader {
            protocol_version: crate::PROTOCOL_VERSION.to_string(),
            sequence_number: 1,
        }),
        timestamp: *ts,
        changes: inc
            .changes
            .iter()
            .map(|c| fxp::MarketOrderUpdate {
                update_type: match c.update_type {
                    MarketUpdateType::NewOrder => 0,
                    MarketUpdateType::ModifyOrder => 1,
                    MarketUpdateType::CancelOrder => 2,
                },
                price: c.price,
                quantity: c.quantity as i32,
                side: md_side(&c.side),
            })
            .collect(),
    }
}

fn build_trade(
    symbol: &str,
    seq: u64,
    px: i64,
    qty: u32,
    aggressor_side: i32,
    is_auction: bool,
    ts: &Option<Timestamp>,
) -> fxp::TradeCaptureReport {
    fxp::TradeCaptureReport {
        header: Some(fxp::FxpHeader {
            protocol_version: crate::PROTOCOL_VERSION.to_string(),
            sequence_number: 0,
        }),
        trade_id: format!("{symbol}:{seq}"),
        buy_order_id: String::new(),
        sell_order_id: String::new(),
        symbol: symbol.to_string(),
        trade_price: px,
        trade_quantity: qty as i32,
        timestamp: *ts,
        aggressor_side,
        seq,
        is_auction,
    }
}

/// Fill reports to each order's own connection, then remainder cancels (after
/// the fills they complete), then market data for rested orders and fills.
fn report_execution<R: Clone>(
    ids: &mut impl ExecIds,
    outputs: &mut Vec<Output<R>>,
    exec: Execution,
    states: &mut HashMap<String, OrderState<R>>,
    symbol: &str,
    ts: &Option<Timestamp>,
    trade_seq: &mut u64,
) {
    let mut stp = exec.stp.iter().peekable();
    for (i, f) in exec.fills.iter().enumerate() {
        while let Some(e) = stp.next_if(|e| e.before_fill <= i) {
            report_stp(ids, outputs, states, symbol, e, ts);
        }
        for id in [&f.buy_order_id, &f.sell_order_id] {
            if let Some((frame, tx)) =
                apply_fill(ids, states, id, symbol, f.trade_price, f.trade_quantity, ts)
            {
                outputs.push(Output::ExecutionReport(tx.clone(), frame));
            }
        }
        *trade_seq += 1;
        outputs.push(Output::Trade(build_trade(
            symbol,
            *trade_seq,
            f.trade_price,
            f.trade_quantity,
            md_side(&f.aggressor_side),
            false,
            ts,
        )));
    }
    for e in stp {
        report_stp(ids, outputs, states, symbol, e, ts);
    }
    for id in &exec.cancels {
        if let Some(st) = states.remove(id) {
            let ev = ReportEvent::new(
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled,
            );
            outputs.push(Output::ExecutionReport(
                st.output_tx.clone(),
                build_report(ids, id, symbol, Some(&st), ev, ts),
            ));
        }
    }
    for inc in exec
        .rested
        .iter()
        .chain(&exec.stp_updates)
        .chain(&exec.fill_updates)
    {
        outputs.push(Output::MarketData(build_md_update(inc, ts)));
    }
}

/// EXEC_CANCELED (or EXEC_RESTATED for a reduced survivor) for a prevented
/// self-match, with the STP mode as the reason.
fn report_stp<R: Clone>(
    ids: &mut impl ExecIds,
    outputs: &mut Vec<Output<R>>,
    states: &mut HashMap<String, OrderState<R>>,
    symbol: &str,
    e: &StpEvent,
    ts: &Option<Timestamp>,
) {
    let reason = format!("self-trade prevention ({})", e.mode.as_str());
    let (st, ev) = match e.action {
        StpAction::Canceled => {
            let Some(st) = states.remove(&e.order_id) else {
                return;
            };
            let ev = ReportEvent::new(
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled,
            );
            (st, ev)
        }
        StpAction::Restated(by) => {
            let Some(st) = states.get_mut(&e.order_id) else {
                return;
            };
            st.order_qty = st.order_qty.saturating_sub(by);
            let status = if st.cum_qty > 0 {
                fxp::OrderStatus::OrderPartiallyFilled
            } else {
                fxp::OrderStatus::OrderNew
            };
            let ev = ReportEvent {
                leaves: st.leaves(),
                ..ReportEvent::new(fxp::ExecutionType::ExecRestated, status)
            };
            (
                OrderState {
                    sender: st.sender.clone(),
                    expire_date: st.expire_date.clone(),
                    account: st.account.clone(),
                    client_order_id: st.client_order_id.clone(),
                    output_tx: st.output_tx.clone(),
                    ..*st
                },
                ev,
            )
        }
    };
    let ev = ReportEvent {
        reject_reason: &reason,
        ..ev
    };
    outputs.push(Output::ExecutionReport(
        st.output_tx.clone(),
        build_report(ids, &e.order_id, symbol, Some(&st), ev, ts),
    ));
}

/// Full-depth snapshot: bids best (highest) first, asks best (lowest) first.
pub(crate) fn snapshot(book: &OrderBook, now: Time) -> fxp::MarketDataSnapshot {
    let snap = book.get_market_snapshot();
    let level = |side: OrderSide| {
        move |&(price, size): &(i64, u32)| fxp::MarketOrderBookEntry {
            price,
            size: size as i32,
            side: md_side(&side),
        }
    };
    fxp::MarketDataSnapshot {
        header: Some(fxp::FxpHeader {
            protocol_version: crate::PROTOCOL_VERSION.to_string(),
            sequence_number: 1,
        }),
        symbol: snap.symbol.clone(),
        bid_levels: snap.bids.iter().rev().map(level(OrderSide::Buy)).collect(),
        ask_levels: snap.asks.iter().map(level(OrderSide::Sell)).collect(),
        timestamp: Some(Timestamp {
            seconds: now.seconds,
            nanos: now.nanos,
        }),
        ..Default::default()
    }
}

#[cfg(test)]
mod expiry_tests {
    use super::*;
    use std::cell::Cell;
    use std::rc::Rc;

    #[derive(Clone)]
    struct FakeClock(Rc<Cell<Time>>);
    impl Clock for FakeClock {
        fn now(&self) -> Time {
            self.0.get()
        }
    }
    struct Ids(u64);
    impl ExecIds for Ids {
        fn next_id(&mut self) -> String {
            self.0 += 1;
            format!("X{}", self.0)
        }
    }
    fn order(id: &str, tif: fxp::TimeInForce, date: &str) -> fxp::NewOrderSingle {
        fxp::NewOrderSingle {
            order_id: id.into(),
            sender: "owner".into(),
            symbol: "TEST".into(),
            client_order_id: format!("client-{id}"),
            account: "account".into(),
            side: fxp::OrderSide::Buy as i32,
            order_type: fxp::OrderType::Limit as i32,
            time_in_force: tif as i32,
            expire_date: date.into(),
            price: 100,
            quantity: 10,
            ..Default::default()
        }
    }
    fn reports(outputs: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
        outputs
            .iter()
            .filter_map(|o| match o {
                Output::ExecutionReport(owner, r) => {
                    assert_eq!(*owner, 7);
                    Some(r)
                }
                _ => None,
            })
            .collect()
    }
    #[test]
    fn gtd_rejects_past_missing_and_malformed_dates() {
        let clock = FakeClock(Rc::new(Cell::new(Time {
            seconds: 86_400,
            nanos: 0,
        })));
        let mut engine = Engine::new("TEST", clock, Ids(0), None);
        for date in [
            "1970-01-01",
            "",
            "2025-02-29",
            "2024-2-01",
            "2024-01-01junk",
            "0000-01-01",
        ] {
            let outputs = engine.new_order(order("bad", fxp::TimeInForce::Gtd, date), 7);
            let r = reports(&outputs);
            assert_eq!(r.len(), 1);
            assert_eq!(
                r[0].execution_type,
                fxp::ExecutionType::ExecRejected as i32,
                "{date}"
            );
            assert!(!r[0].reject_reason.is_empty());
            assert!(engine.states.is_empty());
            assert!(engine.book.order_lookup.is_empty());
        }
    }

    fn setup(schedule: Option<&str>) -> (Engine<u32, FakeClock, Ids>, FakeClock) {
        let clock = FakeClock(Rc::new(Cell::new(Time {
            seconds: 0,
            nanos: 0,
        })));
        let engine = Engine::new(
            "TEST",
            clock.clone(),
            Ids(0),
            schedule.map(|s| s.parse().unwrap()),
        );
        (engine, clock)
    }
    fn advance(clock: &FakeClock, seconds: i64, nanos: i32) {
        clock.0.set(Time { seconds, nanos });
    }
    fn expired(outputs: &[Output<u32>], ids: &[&str]) {
        let r = reports(outputs);
        assert_eq!(
            r.iter().map(|r| r.order_id.as_str()).collect::<Vec<_>>(),
            ids
        );
        for report in r {
            assert_eq!(
                report.execution_type,
                fxp::ExecutionType::ExecExpired as i32
            );
            assert_eq!(report.order_status, fxp::OrderStatus::OrderExpired as i32);
            assert_eq!(report.leaves_quantity, 0);
        }
    }
    #[test]
    fn expiry_at_boundary_precedes_a_crossing_order_and_is_not_repeated() {
        for (tif, date, schedule, boundary) in [
            (fxp::TimeInForce::Day, "", Some("close=00:01"), 60),
            (fxp::TimeInForce::Gtd, "1970-01-01", None, 86_400),
        ] {
            let (mut engine, clock) = setup(schedule);
            engine.new_order(order("EXPIRES", tif, date), 7);
            advance(&clock, boundary - 1, 999_999_999);
            assert!(engine.tick().is_empty());
            advance(&clock, boundary, 0);
            let incoming = fxp::NewOrderSingle {
                side: fxp::OrderSide::Sell as i32,
                order_type: fxp::OrderType::Market as i32,
                ..order("ATBOUNDARY", fxp::TimeInForce::Gtc, "")
            };
            let outputs = engine.new_order(incoming, 7);
            let r = reports(&outputs);
            assert_eq!(r[0].order_id, "EXPIRES");
            assert_eq!(r[0].execution_type, fxp::ExecutionType::ExecExpired as i32);
            assert!(
                r.iter()
                    .all(|r| r.execution_type != fxp::ExecutionType::ExecFilled as i32)
            );
            assert_eq!(
                r[0].timestamp,
                Some(Timestamp {
                    seconds: boundary,
                    nanos: 0
                })
            );
            assert!(engine.tick().is_empty());
            assert!(engine.book.order_lookup.is_empty());
        }
    }
    #[test]
    fn day_expires_at_close_with_cancel_market_data() {
        let (mut engine, clock) = setup(Some("close=00:01:00"));
        engine.new_order(order("D", fxp::TimeInForce::Day, ""), 7);
        advance(&clock, 59, 999_999_999);
        assert!(engine.tick().is_empty());
        advance(&clock, 60, 0);
        let outputs = engine.tick();
        expired(&outputs, &["D"]);
        assert_eq!(outputs.len(), 2);
        let Output::MarketData(md) = &outputs[1] else {
            panic!("missing MD");
        };
        assert_eq!(md.changes.len(), 1);
        assert_eq!(
            (
                md.changes[0].update_type,
                md.changes[0].price,
                md.changes[0].quantity
            ),
            (2, 100, 10)
        );
        assert_eq!(
            md.timestamp,
            Some(Timestamp {
                seconds: 60,
                nanos: 0
            })
        );
        assert!(engine.book.order_lookup.is_empty());
        assert!(engine.states.is_empty());
    }
    #[test]
    fn partial_fill_expiry_preserves_cumulative_fields() {
        let (mut engine, clock) = setup(Some("close=00:01"));
        engine.new_order(order("D", fxp::TimeInForce::Day, ""), 7);
        let sell = fxp::NewOrderSingle {
            side: fxp::OrderSide::Sell as i32,
            quantity: 4,
            order_type: fxp::OrderType::Market as i32,
            ..order("S", fxp::TimeInForce::Day, "")
        };
        engine.new_order(sell, 7);
        advance(&clock, 60, 0);
        let outputs = engine.tick();
        expired(&outputs, &["D"]);
        let r = reports(&outputs)[0];
        assert_eq!((r.cum_quantity, r.avg_px, r.order_quantity), (4, 100, 10));
        assert_eq!(
            (r.client_order_id.as_str(), r.account.as_str()),
            ("client-D", "account")
        );
        assert_eq!(r.side, fxp::OrderSide::Buy as i32);
    }
    #[test]
    fn gtd_expires_at_next_midnight_without_schedule() {
        let (mut engine, clock) = setup(None);
        engine.new_order(order("G", fxp::TimeInForce::Gtd, "1970-01-01"), 7);
        advance(&clock, 86_399, 999_999_999);
        assert!(engine.tick().is_empty());
        advance(&clock, 86_400, 0);
        expired(&engine.tick(), &["G"]);
        assert!(engine.tick().is_empty());
    }
    #[test]
    fn parked_day_and_gtd_stops_expire_without_market_data() {
        let (mut engine, clock) = setup(Some("close=00:01"));
        for (id, tif, date, ty) in [
            ("D", fxp::TimeInForce::Day, "", fxp::OrderType::Stop),
            (
                "G",
                fxp::TimeInForce::Gtd,
                "1970-01-01",
                fxp::OrderType::StopLimit,
            ),
        ] {
            engine.new_order(
                fxp::NewOrderSingle {
                    order_type: ty as i32,
                    stop_price: 150,
                    ..order(id, tif, date)
                },
                7,
            );
        }
        advance(&clock, 60, 0);
        let outputs = engine.tick();
        expired(&outputs, &["D"]);
        assert_eq!(outputs.len(), 1);
        advance(&clock, 86_400, 0);
        let outputs = engine.tick();
        expired(&outputs, &["G"]);
        assert_eq!(outputs.len(), 1);
        assert!(engine.stop_book.is_empty());
        assert!(engine.states.is_empty());
    }
    #[test]
    fn jump_orders_boundaries_then_arrivals_and_expires_only_once() {
        let (mut engine, clock) = setup(Some("close=01:00"));
        for (id, tif, date) in [
            ("G2", fxp::TimeInForce::Gtd, "1970-01-02"),
            ("D2", fxp::TimeInForce::Day, ""),
            ("D1", fxp::TimeInForce::Day, ""),
            ("G1", fxp::TimeInForce::Gtd, "1970-01-01"),
            ("GTC", fxp::TimeInForce::Gtc, "garbage"),
        ] {
            engine.new_order(order(id, tif, date), 7);
        }
        advance(&clock, 4 * 86_400, 0);
        expired(&engine.tick(), &["D2", "D1", "G1", "G2"]);
        assert!(engine.tick().is_empty());
        assert_eq!(engine.states.len(), 1);
        assert!(engine.book.order_lookup.contains_key("GTC"));
    }
    #[test]
    fn day_without_schedule_never_expires() {
        let (mut engine, clock) = setup(None);
        engine.new_order(order("D", fxp::TimeInForce::Day, "garbage"), 7);
        advance(&clock, 30 * 86_400, 0);
        assert!(engine.tick().is_empty());
        assert!(engine.states.contains_key("D"));
    }
    fn replace(id: &str, new_id: &str, tif: fxp::TimeInForce) -> CancelReplaceMsg<u32> {
        CancelReplaceMsg {
            new_display_quantity: None,
            order_id: id.into(),
            sender: "owner".into(),
            new_order_id: new_id.into(),
            client_replace_id: "R".into(),
            new_price: 0,
            new_quantity: 0,
            new_time_in_force: tif as i32,
            new_stop_price: 0,
            output_tx: 7,
        }
    }
    #[test]
    fn replace_preserves_date_and_validates_gtd_transition() {
        let (mut engine, clock) = setup(None);
        engine.new_order(order("G", fxp::TimeInForce::Gtd, "1970-01-01"), 7);
        engine.cancel_replace(replace("G", "GR", fxp::TimeInForce::Unspecified));
        engine.new_order(order("D", fxp::TimeInForce::Day, ""), 7);
        let outputs = engine.cancel_replace(replace("D", "DR", fxp::TimeInForce::Gtd));
        assert_eq!(
            reports(&outputs)[0].execution_type,
            fxp::ExecutionType::ExecRejected as i32
        );
        assert!(engine.book.order_lookup.contains_key("D"));
        advance(&clock, 86_400, 0);
        expired(&engine.tick(), &["GR"]);
    }
    #[test]
    fn every_input_advances_before_handling_request() {
        for kind in 0..4 {
            let (mut engine, clock) = setup(Some("close=00:01"));
            engine.new_order(order("D", fxp::TimeInForce::Day, ""), 7);
            advance(&clock, 60, 0);
            let outputs = match kind {
                0 => engine.new_order(order("N", fxp::TimeInForce::Gtc, ""), 7),
                1 => engine.cancel(CancelMsg {
                    order_id: "D".into(),
                    sender: "owner".into(),
                    client_cancel_id: "C".into(),
                    output_tx: 7,
                }),
                2 => engine.cancel_replace(replace("D", "DR", fxp::TimeInForce::Unspecified)),
                _ => engine.subscribe(7),
            };
            expired(&outputs[..2], &["D"]);
            assert!(!engine.book.order_lookup.contains_key("D"));
            if kind == 3 {
                let Output::Snapshot(_, snap) = &outputs[2] else {
                    panic!("snapshot missing");
                };
                assert!(snap.bid_levels.is_empty());
            }
        }
    }
    #[test]
    fn schedule_parsing_is_strict() {
        for valid in ["close=00:00", "close=23:59:59", "close=16:30"] {
            assert!(valid.parse::<SessionSchedule>().is_ok(), "{valid}");
        }
        for invalid in [
            "",
            "close=24:00",
            "close=12:60",
            "close=12:00:60",
            "close=1:00",
            "close=12:00,",
            "close=12:00,close=13:00",
            "open=09:00",
            "close=12:00junk",
            "close =12:00",
            "close=12:00=13:00",
            "close=+1:00",
        ] {
            assert!(invalid.parse::<SessionSchedule>().is_err(), "{invalid}");
        }
    }

    #[test]
    fn expiry_dates_use_gregorian_utc_midnights() {
        assert_eq!(expiry_instant("1970-01-01").unwrap(), 86_400);
        assert_eq!(expiry_instant("2000-02-29").unwrap(), 951_868_800);
        assert_eq!(expiry_instant("2024-02-29").unwrap(), 1_709_251_200);
        assert!(expiry_instant("1900-02-29").is_err());
        assert!(expiry_instant("2100-02-29").is_err());
        assert!(expiry_instant("2024-04-31").is_err());
        assert!(expiry_instant("2024-00-01").is_err());
    }

    #[test]
    fn parked_replacement_retains_date_and_arrives_after_older_orders() {
        let (mut engine, clock) = setup(None);
        for id in ["first", "second"] {
            engine.new_order(
                fxp::NewOrderSingle {
                    order_type: fxp::OrderType::Stop as i32,
                    stop_price: 150,
                    ..order(
                        id,
                        if id == "first" {
                            fxp::TimeInForce::Day
                        } else {
                            fxp::TimeInForce::Gtd
                        },
                        "1970-01-01",
                    )
                },
                7,
            );
        }
        engine.cancel_replace(replace("first", "replaced", fxp::TimeInForce::Gtd));
        advance(&clock, 86_400, 0);
        let outputs = engine.tick();
        expired(&outputs, &["second", "replaced"]);
        assert_eq!(outputs.len(), 2);
        assert!(engine.stop_book.is_empty());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct FakeClock;
    impl Clock for FakeClock {
        fn now(&self) -> Time {
            Time {
                seconds: 123,
                nanos: 456_000_000,
            }
        }
    }
    struct TestIds(u64);
    impl ExecIds for TestIds {
        fn next_id(&mut self) -> String {
            self.0 += 1;
            format!("X{}", self.0)
        }
    }
    fn engine() -> Engine<u32, FakeClock, TestIds> {
        Engine::new("TEST", FakeClock, TestIds(0), None)
    }
    fn order(id: &str, side: fxp::OrderSide) -> fxp::NewOrderSingle {
        fxp::NewOrderSingle {
            order_id: id.into(),
            sender: "T".into(),
            symbol: "TEST".into(),
            side: side as i32,
            order_type: fxp::OrderType::Limit as i32,
            time_in_force: fxp::TimeInForce::Gtc as i32,
            price: 100,
            quantity: 5,
            ..Default::default()
        }
    }
    fn report(output: &Output<u32>, owner: u32, id: &str) -> fxp::ExecutionReport {
        let Output::ExecutionReport(actual_owner, report) = output else {
            panic!("expected execution report");
        };
        assert_eq!(*actual_owner, owner);
        assert_eq!(report.execution_id, id);
        assert_eq!(
            report.timestamp,
            Some(Timestamp {
                seconds: 123,
                nanos: 456_000_000
            })
        );
        report.clone()
    }

    #[test]
    fn resting_ack_then_fill_are_deterministic_and_addressed() {
        let mut engine = engine();
        let outputs = engine.new_order(order("B", fxp::OrderSide::Buy), 1);
        assert_eq!(outputs.len(), 2);
        let ack = report(&outputs[0], 1, "X1");
        assert_eq!(ack.execution_type, fxp::ExecutionType::ExecNew as i32);
        assert_eq!(ack.leaves_quantity, 5);
        assert!(matches!(outputs[1], Output::MarketData(_)));
        assert_eq!(engine.book.order_lookup["B"].timestamp, 123_456);

        let outputs = engine.new_order(order("S", fxp::OrderSide::Sell), 2);
        assert_eq!(outputs.len(), 7);
        let ack = report(&outputs[0], 2, "X2");
        assert_eq!(ack.execution_type, fxp::ExecutionType::ExecNew as i32);
        for (output, owner, id) in [(&outputs[1], 1, "X3"), (&outputs[2], 2, "X4")] {
            let fill = report(output, owner, id);
            assert_eq!(fill.execution_type, fxp::ExecutionType::ExecFilled as i32);
            assert_eq!(fill.execution_price, 100);
            assert_eq!(fill.executed_quantity, 5);
            assert_eq!(fill.leaves_quantity, 0);
        }
        assert!(matches!(outputs[3], Output::Trade(_)));
        assert!(
            outputs[4..]
                .iter()
                .all(|o| matches!(o, Output::MarketData(_)))
        );
        assert!(engine.states.is_empty());
        assert_eq!(engine.last_trade_price, Some(100));
    }

    #[test]
    fn empty_snapshot_and_unknown_cancel_are_deterministic() {
        let mut engine = engine();
        let outputs = engine.subscribe(7);
        let Output::Snapshot(owner, snapshot) = &outputs[0] else {
            panic!("expected snapshot");
        };
        assert_eq!(*owner, 7);
        assert!(snapshot.bid_levels.is_empty());
        assert!(snapshot.ask_levels.is_empty());
        assert_eq!(
            snapshot.timestamp,
            Some(Timestamp {
                seconds: 123,
                nanos: 456_000_000
            })
        );
        let Output::TradingSessionStatus(status) = &outputs[1] else {
            panic!("expected current phase after snapshot");
        };
        assert_eq!(status.phase, fxp::TradingPhase::Continuous as i32);
        assert_eq!(status.timestamp, snapshot.timestamp);
        let outputs = engine.cancel(CancelMsg {
            order_id: "missing".into(),
            sender: "T".into(),
            client_cancel_id: "C".into(),
            output_tx: 7,
        });
        assert_eq!(outputs.len(), 1);
        let reject = report(&outputs[0], 7, "X1");
        assert_eq!(
            reject.execution_type,
            fxp::ExecutionType::ExecRejected as i32
        );
        assert_eq!(reject.orig_order_id, "missing");
        assert!(!reject.reject_reason.is_empty());
    }

    #[test]
    fn invalid_order_does_not_change_book() {
        let mut engine = engine();
        let mut invalid = order("bad", fxp::OrderSide::Buy);
        invalid.quantity = 0;
        let outputs = engine.new_order(invalid, 3);
        assert_eq!(outputs.len(), 1);
        let reject = report(&outputs[0], 3, "X1");
        assert_eq!(reject.reject_reason, "quantity must be positive");
        assert!(engine.states.is_empty());
        assert!(engine.book.order_lookup.is_empty());
    }
}

/// Reserve is supported only in continuous-only limit sessions. Reject before mutation.
fn iceberg_reason(
    display: i32,
    total: u32,
    ty: OrderType,
    tif: TimeInForce,
    full_schedule: bool,
    lot: u32,
) -> Option<String> {
    if display < 0 {
        return Some("display quantity must be nonnegative".into());
    }
    if display == 0 {
        return None;
    }
    if display as u32 > total || !(display as u32).is_multiple_of(lot) {
        return Some(
            "display quantity must not exceed total and must be a multiple of lot size".into(),
        );
    }
    if full_schedule
        || ty != OrderType::Limit
        || !matches!(tif, TimeInForce::Day | TimeInForce::GTC | TimeInForce::Gtd)
    {
        return Some("icebergs require limit DAY/GTC/GTD without auction sessions".into());
    }
    None
}
