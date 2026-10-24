// SPDX-License-Identifier: MIT

use crate::fxp;
use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketUpdateType};
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::{OrderBook, OrderReject, OrderReplace};
use crate::order_book::stops::{Execution, StopBook, StopReplace, run_stop_cascade, stop_fires};
use prost_types::Timestamp;
use std::collections::HashMap;
use std::sync::Arc;

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
    pub output_tx: R,
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

#[derive(Clone, Copy)]
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
    ExecutionReport(R, fxp::ExecutionReport),
    MarketData(fxp::MarketDataIncrementalUpdate),
    Snapshot(R, fxp::MarketDataSnapshot),
}

pub struct Engine<R, C, I> {
    symbol: String,
    book: OrderBook,
    stop_book: StopBook,
    last_trade_price: Option<i64>,
    states: HashMap<String, OrderState<R>>,
    clock: C,
    ids: I,
}

impl<R: Clone, C: Clock, I: ExecIds> Engine<R, C, I> {
    pub fn new(symbol: &str, clock: C, ids: I) -> Self {
        Self {
            symbol: symbol.into(),
            book: OrderBook::new(symbol, Arc::new(MarketDataBroadcaster::new())),
            stop_book: StopBook::default(),
            last_trade_price: None,
            states: HashMap::new(),
            clock,
            ids,
        }
    }
    pub fn new_order(&mut self, order: fxp::NewOrderSingle, owner: R) -> Vec<Output<R>> {
        self.process(SymbolMsg::Order(OrderMsg {
            order,
            output_tx: owner,
        }))
    }
    pub fn cancel(&mut self, request: CancelMsg<R>) -> Vec<Output<R>> {
        self.process(SymbolMsg::Cancel(request))
    }
    pub fn cancel_replace(&mut self, request: CancelReplaceMsg<R>) -> Vec<Output<R>> {
        self.process(SymbolMsg::CancelReplace(request))
    }
    pub fn subscribe(&self, owner: R) -> Vec<Output<R>> {
        vec![Output::Snapshot(
            owner,
            snapshot(&self.book, self.clock.now()),
        )]
    }
    fn process(&mut self, msg: SymbolMsg<R>) -> Vec<Output<R>> {
        let symbol = &self.symbol;
        let book = &mut self.book;
        let stop_book = &mut self.stop_book;
        let last_trade_price = &mut self.last_trade_price;
        let states = &mut self.states;
        let ids = &mut self.ids;
        let mut outputs = Vec::new();
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
                                    side: side_to_proto(replacement.side),
                                    order_qty: replacement.quantity,
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
                                if last_trade_price.is_some_and(|p| stop_fires(&replacement, p)) {
                                    let order = stop_book
                                        .cancel_for(&replacement.order_id, &replacement.trader_id)
                                        .expect("replacement was just parked");
                                    let mut exec = Execution::default();
                                    exec.triggered.push(order.order_id.clone());
                                    book.execute_into(order, &mut exec);
                                    run_stop_cascade(book, stop_book, last_trade_price, &mut exec);
                                    report_execution(ids, &mut outputs, exec, states, symbol, &ts);
                                }
                            }
                        }
                        break 'request;
                    }

                    let result = book.replace_order(
                        &cr_msg.order_id,
                        &cr_msg.new_order_id,
                        OrderReplace {
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
                                side: side_to_proto(replacement.side),
                                order_qty: cum_qty + replacement.quantity,
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

                            // Immediate fills from the replacement can trigger parked stops
                            let (fills, fill_updates) = book.match_orders();
                            let mut exec = Execution {
                                fills,
                                fill_updates,
                                ..Default::default()
                            };
                            run_stop_cascade(book, stop_book, last_trade_price, &mut exec);
                            outputs.push(Output::MarketData(build_md_update(&inc, &ts)));
                            report_execution(ids, &mut outputs, exec, states, symbol, &ts);
                        }
                    }
                }

                SymbolMsg::Order(order_msg) => {
                    let order = order_msg.order;
                    let output_tx = order_msg.output_tx;
                    let now = self.clock.now();

                    let parsed_enums = parse_order_enums(&order);
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
                        _ => None,
                    };
                    if let Some(reason) = invalid {
                        let ts = Some(Timestamp {
                            seconds: now.seconds,
                            nanos: now.nanos,
                        });
                        let state = OrderState::new(
                            &order,
                            order.quantity.max(0) as u32,
                            output_tx.clone(),
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
                    };

                    let state = OrderState::new(&order, order_obj.quantity, output_tx.clone());
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

                        let fires_immediately =
                            last_trade_price.is_some_and(|p| stop_fires(&order_obj, p));

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
                    report_execution(ids, &mut outputs, exec, states, symbol, &ts);
                }
            }
        }
        outputs
    }
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
    side: i32,
    order_qty: u32,
    cum_qty: u32,
    notional: i128,
    account: String,
    client_order_id: String,
    output_tx: R,
}

impl<R: Clone> OrderState<R> {
    fn new(order: &fxp::NewOrderSingle, order_qty: u32, output_tx: R) -> Self {
        Self {
            side: order.side,
            order_qty,
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

/// Fill reports to each order's own connection, then remainder cancels (after
/// the fills they complete), then market data for rested orders and fills.
fn report_execution<R: Clone>(
    ids: &mut impl ExecIds,
    outputs: &mut Vec<Output<R>>,
    exec: Execution,
    states: &mut HashMap<String, OrderState<R>>,
    symbol: &str,
    ts: &Option<Timestamp>,
) {
    for f in &exec.fills {
        for id in [&f.buy_order_id, &f.sell_order_id] {
            if let Some((frame, tx)) =
                apply_fill(ids, states, id, symbol, f.trade_price, f.trade_quantity, ts)
            {
                outputs.push(Output::ExecutionReport(tx.clone(), frame));
            }
        }
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
    for inc in exec.rested.iter().chain(&exec.fill_updates) {
        outputs.push(Output::MarketData(build_md_update(inc, ts)));
    }
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
        Engine::new("TEST", FakeClock, TestIds(0))
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
        assert_eq!(outputs.len(), 5);
        let ack = report(&outputs[0], 2, "X2");
        assert_eq!(ack.execution_type, fxp::ExecutionType::ExecNew as i32);
        for (output, owner, id) in [(&outputs[1], 1, "X3"), (&outputs[2], 2, "X4")] {
            let fill = report(output, owner, id);
            assert_eq!(fill.execution_type, fxp::ExecutionType::ExecFilled as i32);
            assert_eq!(fill.execution_price, 100);
            assert_eq!(fill.executed_quantity, 5);
            assert_eq!(fill.leaves_quantity, 0);
        }
        assert!(
            outputs[3..]
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
