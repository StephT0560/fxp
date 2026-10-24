// SPDX-License-Identifier: MIT

// server.rs — FXP Core TCP server with per-symbol dispatch + lock-free writes.
//
// Architecture:
//
//   handle_tcp_client
//     ├── read_task: reads frames, dispatches to symbol channels (never blocks)
//     └── write_task: drains output_tx channel, writes to TCP socket (no lock)
//
//   order_book_task (one per symbol, shared across all connections)
//     └── sends ExecutionReport frames into the originating connection's
//         output_tx channel — no SharedWriter mutex needed
//
// Why this is faster than the previous version:
//   Before: 25 symbol tasks competed on Arc<Mutex<OwnedWriteHalf>> for writes.
//           Under load this mutex became the new bottleneck after we removed
//           the order book mutex.
//   After:  Symbol tasks send frames into a lockless mpsc channel. One writer
//           task per connection drains the channel sequentially — correct
//           ordering, zero contention.

use prost::Message;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering as AtomicOrdering};
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio::net::tcp::OwnedReadHalf;
use tokio::sync::Mutex;
use tokio::sync::mpsc;

mod fxp {
    include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
}

use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketUpdateType};
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::{OrderBook, OrderReject};
use crate::order_book::stops::{Execution, StopBook, StopReplace, run_stop_cascade, stop_fires};
use prost_types::Timestamp;

// ── Output channel ────────────────────────────────────────────────────────────
// Each connection gets one output channel. All tasks that need to write to
// this connection send frames here. The write_task drains it exclusively.
type OutputTx = mpsc::Sender<Vec<u8>>;

// ── Messages dispatched to a symbol's task ────────────────────────────────────

struct OrderMsg {
    order: fxp::NewOrderSingle,
    output_tx: OutputTx,
}

struct SubscribeMsg {
    tx: mpsc::Sender<Vec<u8>>,
}

struct CancelMsg {
    order_id: String,
    sender: String, // requesting trader; must own the order
    client_cancel_id: String,
    output_tx: OutputTx,
}

struct CancelReplaceMsg {
    order_id: String,
    sender: String, // requesting trader; must own the order
    new_order_id: String,
    client_replace_id: String,
    new_price: i64,    // 0 = no change
    new_quantity: i32, // 0 = no change
    new_time_in_force: i32,
    new_stop_price: i64, // 0 = no change (parked stops only)
    output_tx: OutputTx,
}

enum SymbolMsg {
    Order(OrderMsg),
    Subscribe(SubscribeMsg),
    Cancel(CancelMsg),
    CancelReplace(CancelReplaceMsg),
}

// ── Symbol router ─────────────────────────────────────────────────────────────

#[derive(Clone)]
struct SymbolRouter {
    channels: Arc<Mutex<HashMap<String, mpsc::Sender<SymbolMsg>>>>,
}

impl SymbolRouter {
    fn new() -> Self {
        Self {
            channels: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    async fn get_or_create(&self, symbol: &str) -> mpsc::Sender<SymbolMsg> {
        // Fast path: symbol already exists.
        {
            let map = self.channels.lock().await;
            if let Some(tx) = map.get(symbol) {
                return tx.clone();
            }
        }
        // Slow path: first time seeing this symbol.
        let mut map = self.channels.lock().await;
        // Re-check after acquiring write lock — another task may have raced us.
        if let Some(tx) = map.get(symbol) {
            return tx.clone();
        }
        let (tx, rx) = mpsc::channel::<SymbolMsg>(4096);
        map.insert(symbol.to_string(), tx.clone());
        let sym = symbol.to_string();
        // Spawn while holding the lock to guarantee the entry is visible
        // before any message can be dispatched to this symbol.
        tokio::spawn(order_book_task(sym, rx));
        tx // return cloned sender directly — no need to re-acquire
    }
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

// ── Per-symbol order book task ────────────────────────────────────────────────
// Owns its order book exclusively — no mutex required.
// Writes go into the connection's output channel, not directly to the socket.

async fn order_book_task(symbol: String, mut rx: mpsc::Receiver<SymbolMsg>) {
    let broadcaster = Arc::new(MarketDataBroadcaster::new());
    let mut book = OrderBook::new(&symbol, broadcaster);
    let mut subscribers: Vec<mpsc::Sender<Vec<u8>>> = Vec::new();

    // Stop order state
    let mut stop_book = StopBook::default();
    let mut last_trade_price: Option<i64> = None; // None = no trade yet this session
    // Report state of every live order (resting, parked stop or in-flight)
    let mut states: HashMap<String, OrderState> = HashMap::new();

    println!("📚 Order book task started for {}", symbol);

    while let Some(msg) = rx.recv().await {
        match msg {
            SymbolMsg::Subscribe(sub) => {
                let _ = sub.tx.send(snapshot_frame(&book)).await;
                subscribers.push(sub.tx);
                println!("✅ Client subscribed to {}", symbol);
            }

            SymbolMsg::Cancel(cancel_msg) => {
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                let ts = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos: now.subsec_nanos() as i32,
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
                        let report =
                            build_report(&cancel_msg.order_id, &symbol, state.as_ref(), ev, &ts);
                        let _ = cancel_msg.output_tx.send(report).await;

                        // Parked stops are not on the book: no market data to broadcast
                        if let Some(md_update) = md_update {
                            broadcast_md(&mut subscribers, md_frame(&md_update, &ts)).await;
                        }
                        println!("🗑️ Cancelled order {} on {}", cancel_msg.order_id, symbol);
                    }
                    Err(reject) => {
                        // Not live (filled, cancelled, unknown) or not owned by the requester
                        let report = make_reject_report(
                            &cancel_msg.order_id,
                            &symbol,
                            &cancel_msg.client_cancel_id,
                            reject.reason(),
                            &ts,
                        );
                        let _ = cancel_msg.output_tx.send(report).await;
                        println!(
                            "⚠️ Cancel rejected: order {} on {}: {:?}",
                            cancel_msg.order_id, symbol, reject
                        );
                    }
                }
            }

            SymbolMsg::CancelReplace(cr_msg) => {
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                let ts = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos: now.subsec_nanos() as i32,
                });

                let new_time_in_force = match parse_replace_time_in_force(cr_msg.new_time_in_force)
                {
                    Ok(value) => value,
                    Err(reason) => {
                        let report = make_reject_report(
                            &cr_msg.order_id,
                            &symbol,
                            &cr_msg.client_replace_id,
                            &reason,
                            &ts,
                        );
                        let _ = cr_msg.output_tx.send(report).await;
                        println!("⚠️ CancelReplace rejected: {}", reason);
                        continue;
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
                        timestamp: now.as_millis() as u64,
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
                                &cr_msg.order_id,
                                &symbol,
                                &cr_msg.client_replace_id,
                                reject.reason(),
                                &ts,
                            );
                            let _ = cr_msg.output_tx.send(report).await;
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
                            send_replace_reports(&cr_msg, &symbol, &new_state, &ts).await;
                            states.insert(cr_msg.new_order_id.clone(), new_state);
                            // A new stop price may already be through the last print
                            if last_trade_price.is_some_and(|p| stop_fires(&replacement, p)) {
                                let order = stop_book
                                    .cancel_for(&replacement.order_id, &replacement.trader_id)
                                    .expect("replacement was just parked");
                                let mut exec = Execution::default();
                                exec.triggered.push(order.order_id.clone());
                                book.execute_into(order, &mut exec);
                                run_stop_cascade(
                                    &mut book,
                                    &mut stop_book,
                                    &mut last_trade_price,
                                    &mut exec,
                                );
                                report_execution(exec, &mut states, &mut subscribers, &symbol, &ts)
                                    .await;
                            }
                            println!(
                                "🔄 CancelReplaced parked stop {} → {} on {}",
                                cr_msg.order_id, cr_msg.new_order_id, symbol
                            );
                        }
                    }
                    continue;
                }

                let result = book.replace_order(
                    &cr_msg.order_id,
                    &cr_msg.new_order_id,
                    cr_msg.new_price,
                    cr_msg.new_quantity.max(0) as u32,
                    new_time_in_force,
                    &cr_msg.client_replace_id,
                    &cr_msg.sender,
                    now.as_millis() as u64,
                );

                match result {
                    Err(reject) => {
                        let report = make_reject_report(
                            &cr_msg.order_id,
                            &symbol,
                            &cr_msg.client_replace_id,
                            reject.reason(),
                            &ts,
                        );
                        let _ = cr_msg.output_tx.send(report).await;
                        println!(
                            "⚠️ CancelReplace rejected: order {} on {}: {:?}",
                            cr_msg.order_id, symbol, reject
                        );
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

                        send_replace_reports(&cr_msg, &symbol, &new_state, &ts).await;
                        states.insert(cr_msg.new_order_id.clone(), new_state);

                        // Immediate fills from the replacement can trigger parked stops
                        let (fills, fill_updates) = book.match_orders();
                        let mut exec = Execution {
                            fills,
                            fill_updates,
                            ..Default::default()
                        };
                        run_stop_cascade(
                            &mut book,
                            &mut stop_book,
                            &mut last_trade_price,
                            &mut exec,
                        );
                        broadcast_md(&mut subscribers, md_frame(&inc, &ts)).await;
                        report_execution(exec, &mut states, &mut subscribers, &symbol, &ts).await;

                        println!(
                            "🔄 CancelReplaced {} → {} on {}",
                            cr_msg.order_id, cr_msg.new_order_id, symbol
                        );
                    }
                }
            }

            SymbolMsg::Order(order_msg) => {
                let order = order_msg.order;
                let output_tx = order_msg.output_tx;
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();

                let parsed_enums = parse_order_enums(&order);
                let is_limit = match &parsed_enums {
                    Ok((_, order_type, _)) => {
                        matches!(order_type, OrderType::Limit | OrderType::StopLimit)
                    }
                    Err(_) => false,
                };
                let invalid = match &parsed_enums {
                    Err(reason) => Some(reason.clone()),
                    Ok(_) if order.quantity <= 0 => Some("quantity must be positive".to_string()),
                    Ok(_) if is_limit && order.price <= 0 => {
                        Some("limit price must be positive".to_string())
                    }
                    _ => None,
                };
                if let Some(reason) = invalid {
                    let ts = Some(Timestamp {
                        seconds: now.as_secs() as i64,
                        nanos: now.subsec_nanos() as i32,
                    });
                    let state =
                        OrderState::new(&order, order.quantity.max(0) as u32, output_tx.clone());
                    let ev = ReportEvent {
                        reject_reason: &reason,
                        ..ReportEvent::new(
                            fxp::ExecutionType::ExecRejected,
                            fxp::OrderStatus::OrderRejected,
                        )
                    };
                    let _ = output_tx
                        .send(build_report(
                            &order.order_id,
                            &symbol,
                            Some(&state),
                            ev,
                            &ts,
                        ))
                        .await;
                    println!("⚠️ Order {} rejected: {}", order.order_id, reason);
                    continue;
                }

                let (side, order_type, time_in_force) =
                    parsed_enums.expect("enum values validated");
                let order_obj = Order {
                    order_id: order.order_id.clone(),
                    trader_id: order.sender.clone(),
                    symbol: symbol.clone(),
                    price: order.price,
                    quantity: order.quantity as u32,
                    side,
                    order_type,
                    time_in_force,
                    timestamp: now.as_millis() as u64,
                    stop_price: order.stop_price,
                    expire_date: order.expire_date.clone(),
                    account: order.account.clone(),
                    client_order_id: order.client_order_id.clone(),
                };

                let state = OrderState::new(&order, order_obj.quantity, output_tx.clone());
                let ts = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos: now.subsec_nanos() as i32,
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
                        let report =
                            build_report(&order_obj.order_id, &symbol, Some(&state), ev, &ts);
                        let _ = output_tx.send(report).await;
                        println!(
                            "⚠️ Stop order {} rejected: stop_price is required",
                            order_obj.order_id
                        );
                        continue;
                    }

                    let fires_immediately =
                        last_trade_price.is_some_and(|p| stop_fires(&order_obj, p));

                    if fires_immediately {
                        println!(
                            "⚡ Stop {} triggered immediately at last_trade={:?}",
                            order_obj.order_id, last_trade_price
                        );
                    } else {
                        // Park in stop_book — send EXEC_NEW acknowledgement
                        let ev = ReportEvent {
                            leaves: state.leaves(),
                            ..ReportEvent::new(
                                fxp::ExecutionType::ExecNew,
                                fxp::OrderStatus::OrderNew,
                            )
                        };
                        let ack = build_report(&order_obj.order_id, &symbol, Some(&state), ev, &ts);
                        let _ = output_tx.send(ack).await;
                        println!(
                            "🛑 Stop {} parked on {} (stop={} last_trade={:?})",
                            order.order_id, symbol, order_obj.stop_price, last_trade_price
                        );
                        states.insert(order_obj.order_id.clone(), state);
                        stop_book.park(order_obj);
                        continue;
                    }
                }

                // Process the order (non-stop, or stop that triggered immediately)
                states.insert(order_obj.order_id.clone(), state);
                let mut exec = Execution::default();
                if book.execute_into(order_obj.clone(), &mut exec) {
                    if let Some(st) = states.get(&order_obj.order_id) {
                        let ev = ReportEvent {
                            leaves: st.leaves(),
                            ..ReportEvent::new(
                                fxp::ExecutionType::ExecNew,
                                fxp::OrderStatus::OrderNew,
                            )
                        };
                        let _ = output_tx
                            .send(build_report(
                                &order_obj.order_id,
                                &symbol,
                                Some(st),
                                ev,
                                &ts,
                            ))
                            .await;
                    }
                }
                run_stop_cascade(&mut book, &mut stop_book, &mut last_trade_price, &mut exec);
                report_execution(exec, &mut states, &mut subscribers, &symbol, &ts).await;
            }
        }
    }

    println!("📚 Order book task for {} exiting", symbol);
}

// ── ExecutionReport builder ───────────────────────────────────────────────────

static NEXT_EXEC_ID: AtomicU64 = AtomicU64::new(1);

/// Engine-wide monotonic execution id, unique across fills at the same price.
fn next_exec_id() -> String {
    format!("X{}", NEXT_EXEC_ID.fetch_add(1, AtomicOrdering::Relaxed))
}

fn side_to_proto(side: OrderSide) -> i32 {
    match side {
        OrderSide::Buy => fxp::OrderSide::Buy as i32,
        OrderSide::Sell => fxp::OrderSide::Sell as i32,
    }
}

/// Report state of a live order: original quantity, running fills and the
/// connection of its owner.
struct OrderState {
    side: i32,
    order_qty: u32,
    cum_qty: u32,
    notional: i128,
    account: String,
    client_order_id: String,
    output_tx: OutputTx,
}

impl OrderState {
    fn new(order: &fxp::NewOrderSingle, order_qty: u32, output_tx: OutputTx) -> Self {
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

fn build_report(
    order_id: &str,
    symbol: &str,
    state: Option<&OrderState>,
    ev: ReportEvent,
    ts: &Option<Timestamp>,
) -> Vec<u8> {
    let client_order_id = ev
        .client_order_id
        .map(str::to_string)
        .or_else(|| state.map(|s| s.client_order_id.clone()))
        .unwrap_or_default();
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::ExecutionReport(
            fxp::ExecutionReport {
                header: Some(fxp::FxpHeader {
                    protocol_version: crate::PROTOCOL_VERSION.to_string(),
                    sequence_number: 0,
                }),
                execution_id: next_exec_id(),
                order_id: order_id.to_string(),
                symbol: symbol.to_string(),
                execution_type: ev.exec_type.into(),
                order_status: ev.status.into(),
                execution_price: ev.last_px,
                executed_quantity: ev.last_qty as i32,
                timestamp: ts.clone(),
                leaves_quantity: ev.leaves as i32,
                avg_px: state.map_or(0, |s| s.avg_px()),
                cum_quantity: state.map_or(0, |s| s.cum_qty as i32),
                account: state.map_or_else(String::new, |s| s.account.clone()),
                client_order_id,
                orig_order_id: ev.orig_order_id.to_string(),
                reject_reason: ev.reject_reason.to_string(),
                side: state.map_or(0, |s| s.side),
                order_quantity: state.map_or(0, |s| s.order_qty as i32),
            },
        )),
    })
}

/// Apply a fill to a live order and build its report, addressed to the
/// order owner's connection. Fully-filled orders leave `states`.
fn apply_fill(
    states: &mut HashMap<String, OrderState>,
    order_id: &str,
    symbol: &str,
    px: i64,
    qty: u32,
    ts: &Option<Timestamp>,
) -> Option<(Vec<u8>, OutputTx)> {
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
    let frame = build_report(order_id, symbol, Some(&*st), ev, ts);
    let tx = st.output_tx.clone();
    if leaves == 0 {
        states.remove(order_id);
    }
    Some((frame, tx))
}

/// Execution report rejecting a cancel or cancel/replace request. orig_order_id
/// marks it as a request reject: the target order is unchanged.
fn make_reject_report(
    order_id: &str,
    symbol: &str,
    request_id: &str,
    reason: &str,
    ts: &Option<Timestamp>,
) -> Vec<u8> {
    let ev = ReportEvent {
        client_order_id: Some(request_id),
        orig_order_id: order_id,
        reject_reason: reason,
        ..ReportEvent::new(
            fxp::ExecutionType::ExecRejected,
            fxp::OrderStatus::OrderRejected,
        )
    };
    build_report(order_id, symbol, None, ev, ts)
}

// ── Market data broadcast ─────────────────────────────────────────────────────

async fn broadcast_md(subscribers: &mut Vec<mpsc::Sender<Vec<u8>>>, frame: Vec<u8>) {
    let mut dead = vec![];
    for (i, tx) in subscribers.iter().enumerate() {
        if tx.send(frame.clone()).await.is_err() {
            dead.push(i);
        }
    }
    for i in dead.into_iter().rev() {
        subscribers.remove(i);
    }
}

// ── Wire helpers ──────────────────────────────────────────────────────────────

async fn read_framed_message(read_half: &mut OwnedReadHalf) -> Result<Vec<u8>, std::io::Error> {
    let mut len_buf = [0u8; 4];
    read_half.read_exact(&mut len_buf).await?;
    let msg_len = u32::from_be_bytes(len_buf) as usize;
    if msg_len == 0 || msg_len > 1_048_576 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("Invalid FXP message length: {}", msg_len),
        ));
    }
    let mut body = vec![0u8; msg_len];
    read_half.read_exact(&mut body).await?;
    Ok(body)
}

fn frame_message(msg: &fxp::FxpMessage) -> Vec<u8> {
    let mut encoded = Vec::new();
    msg.encode(&mut encoded).unwrap();
    let mut framed = (encoded.len() as u32).to_be_bytes().to_vec();
    framed.extend_from_slice(&encoded);
    framed
}

fn md_side(side: &OrderSide) -> i32 {
    match side {
        OrderSide::Buy => fxp::OrderSide::Buy as i32,
        OrderSide::Sell => fxp::OrderSide::Sell as i32,
    }
}

/// EXEC_REPLACED for the original order, EXEC_NEW for the replacement.
async fn send_replace_reports(
    cr_msg: &CancelReplaceMsg,
    symbol: &str,
    new_state: &OrderState,
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
        let _ = cr_msg
            .output_tx
            .send(build_report(id, &symbol, Some(new_state), ev, ts))
            .await;
    }
}

fn md_frame(inc: &MarketDataIncrementalUpdate, ts: &Option<Timestamp>) -> Vec<u8> {
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
            fxp::MarketDataIncrementalUpdate {
                symbol: inc.symbol.clone(),
                header: Some(fxp::FxpHeader {
                    protocol_version: crate::PROTOCOL_VERSION.to_string(),
                    sequence_number: 1,
                }),
                timestamp: ts.clone(),
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
            },
        )),
    })
}

/// Fill reports to each order's own connection, then remainder cancels (after
/// the fills they complete), then market data for rested orders and fills.
async fn report_execution(
    exec: Execution,
    states: &mut HashMap<String, OrderState>,
    subscribers: &mut Vec<mpsc::Sender<Vec<u8>>>,
    symbol: &str,
    ts: &Option<Timestamp>,
) {
    for f in &exec.fills {
        for id in [&f.buy_order_id, &f.sell_order_id] {
            if let Some((frame, tx)) =
                apply_fill(states, id, symbol, f.trade_price, f.trade_quantity, ts)
            {
                let _ = tx.send(frame).await;
            }
        }
    }
    for id in &exec.cancels {
        if let Some(st) = states.remove(id) {
            let ev = ReportEvent::new(
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled,
            );
            let _ = st
                .output_tx
                .send(build_report(id, symbol, Some(&st), ev, ts))
                .await;
        }
    }
    for inc in exec.rested.iter().chain(&exec.fill_updates) {
        broadcast_md(subscribers, md_frame(inc, ts)).await;
    }
}

/// Full-depth snapshot: bids best (highest) first, asks best (lowest) first.
fn snapshot_frame(book: &OrderBook) -> Vec<u8> {
    let snap = book.get_market_snapshot();
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
    let level = |side: OrderSide| {
        move |&(price, size): &(i64, u32)| fxp::MarketOrderBookEntry {
            price,
            size: size as i32,
            side: md_side(&side),
        }
    };
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataSnapshot(
            fxp::MarketDataSnapshot {
                header: Some(fxp::FxpHeader {
                    protocol_version: crate::PROTOCOL_VERSION.to_string(),
                    sequence_number: 1,
                }),
                symbol: snap.symbol.clone(),
                bid_levels: snap.bids.iter().rev().map(level(OrderSide::Buy)).collect(),
                ask_levels: snap.asks.iter().map(level(OrderSide::Sell)).collect(),
                timestamp: Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos: now.subsec_nanos() as i32,
                }),
                ..Default::default()
            },
        )),
    })
}

// ── TCP connection handler ────────────────────────────────────────────────────

struct AbortOnDrop(tokio::task::JoinHandle<()>);

impl Drop for AbortOnDrop {
    fn drop(&mut self) {
        self.0.abort();
    }
}

async fn handle_tcp_client(stream: tokio::net::TcpStream, router: SymbolRouter) {
    println!("🧩 New TCP client connected.");

    let (mut read_half, write_half) = stream.into_split();

    // Output channel: all tasks wanting to write to this connection send here.
    // Capacity 8192: large enough to absorb bursts, bounded to signal back-pressure.
    let (output_tx, mut output_rx) = mpsc::channel::<Vec<u8>>(8192);

    // Write task: drains the output channel and writes to the TCP socket.
    // Single consumer = no locking needed on the write half.
    let writer = tokio::spawn(async move {
        let mut w = write_half;
        while let Some(frame) = output_rx.recv().await {
            if w.write_all(&frame).await.is_err() {
                break;
            }
        }
        println!("🔌 Write task exiting for connection");
    });
    // When the client leaves, drop its receiver so reports for its resting
    // orders fail fast instead of filling a channel nobody drains.
    let _writer = AbortOnDrop(writer);

    // Read loop: dispatches frames to symbol tasks, never blocks on processing.
    loop {
        let body = match read_framed_message(&mut read_half).await {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => {
                println!("🔌 Client disconnected.");
                return;
            }
            Err(e) => {
                println!("❌ Read error: {:?}", e);
                return;
            }
        };

        match fxp::FxpMessage::decode(body.as_slice()) {
            Ok(msg) => match msg.payload {
                Some(fxp::fxp_message::Payload::NewOrder(order)) => {
                    let symbol = order.symbol.clone();
                    let tx = router.get_or_create(&symbol).await;
                    if tx
                        .send(SymbolMsg::Order(OrderMsg {
                            order,
                            output_tx: output_tx.clone(),
                        }))
                        .await
                        .is_err()
                    {
                        println!("❌ Symbol task for {} dropped — channel closed", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::OrderCancel(cancel)) => {
                    let symbol = cancel.symbol.clone();
                    let tx = router.get_or_create(&symbol).await;
                    if tx
                        .send(SymbolMsg::Cancel(CancelMsg {
                            order_id: cancel.order_id.clone(),
                            sender: cancel.sender.clone(),
                            client_cancel_id: cancel.client_cancel_id.clone(),
                            output_tx: output_tx.clone(),
                        }))
                        .await
                        .is_err()
                    {
                        println!("❌ Symbol task for {} dropped on cancel", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::OrderCancelReplace(cr)) => {
                    let symbol = cr.symbol.clone();
                    let tx = router.get_or_create(&symbol).await;
                    if tx
                        .send(SymbolMsg::CancelReplace(CancelReplaceMsg {
                            order_id: cr.order_id.clone(),
                            sender: cr.sender.clone(),
                            new_order_id: cr.new_order_id.clone(),
                            client_replace_id: cr.client_replace_id.clone(),
                            new_price: cr.new_price,
                            new_quantity: cr.new_quantity,
                            new_time_in_force: cr.new_time_in_force,
                            new_stop_price: cr.new_stop_price,
                            output_tx: output_tx.clone(),
                        }))
                        .await
                        .is_err()
                    {
                        println!("❌ Symbol task for {} dropped on cancel/replace", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    println!("📡 MarketDataRequest for {:?}", request.symbols);
                    for symbol in &request.symbols {
                        // Per-subscriber market data channel
                        let (md_tx, mut md_rx) = mpsc::channel::<Vec<u8>>(256);
                        let sym_tx = router.get_or_create(symbol).await;
                        let _ = sym_tx
                            .send(SymbolMsg::Subscribe(SubscribeMsg { tx: md_tx }))
                            .await;

                        // Forward market data frames into the connection's output channel.
                        // No lock needed — output_tx is just an mpsc sender clone.
                        let out = output_tx.clone();
                        tokio::spawn(async move {
                            while let Some(frame) = md_rx.recv().await {
                                if out.send(frame).await.is_err() {
                                    break;
                                }
                            }
                        });
                    }
                    println!("🧼 Subscription setup complete — continuing to accept orders.");
                }

                Some(other) => println!("⚠️ Unhandled payload: {:?}", other),
                None => println!("❌ FXPMessage had no payload"),
            },
            Err(e) => println!("❌ Decode failed: {:?}", e),
        }
    }
}

const ACCEPT_BACKOFF_MIN: std::time::Duration = std::time::Duration::from_millis(5);
const ACCEPT_BACKOFF_MAX: std::time::Duration = std::time::Duration::from_secs(1);

fn next_accept_backoff(current: std::time::Duration) -> std::time::Duration {
    (current * 2).min(ACCEPT_BACKOFF_MAX)
}

// ── Entry point ───────────────────────────────────────────────────────────────

pub async fn start_server() {
    let listener = TcpListener::bind("127.0.0.1:8080").await.unwrap();
    let router = SymbolRouter::new();

    println!("🚀 FXP Core running on 127.0.0.1:8080");
    println!("   Architecture: per-symbol tasks + lock-free output channels");

    let mut backoff = ACCEPT_BACKOFF_MIN;
    loop {
        match listener.accept().await {
            Ok((stream, _)) => {
                backoff = ACCEPT_BACKOFF_MIN;
                let router_clone = router.clone();
                tokio::spawn(async move {
                    handle_tcp_client(stream, router_clone).await;
                });
            }
            // Transient errors (EMFILE, ECONNABORTED, ...) must not kill the server.
            Err(e) => {
                eprintln!("❌ accept failed: {e}; retrying in {backoff:?}");
                tokio::time::sleep(backoff).await;
                backoff = next_accept_backoff(backoff);
            }
        }
    }
}
#[cfg(test)]
mod report_tests {
    use super::*;
    use std::collections::HashSet;
    use std::time::Duration;

    type Rx = mpsc::Receiver<Vec<u8>>;

    fn spawn_book() -> mpsc::Sender<SymbolMsg> {
        let (tx, rx) = mpsc::channel(64);
        tokio::spawn(order_book_task("TEST".to_string(), rx));
        tx
    }

    fn conn() -> (OutputTx, Rx) {
        mpsc::channel(64)
    }

    #[allow(clippy::too_many_arguments)]
    fn nos(
        id: &str,
        trader: &str,
        side: i32,
        px: i64,
        qty: i32,
        ord_type: i32,
        tif: i32,
    ) -> fxp::NewOrderSingle {
        fxp::NewOrderSingle {
            order_id: id.to_string(),
            sender: trader.to_string(),
            symbol: "TEST".to_string(),
            price: px,
            side,
            quantity: qty,
            order_type: ord_type,
            time_in_force: tif,
            account: format!("ACC-{trader}"),
            client_order_id: format!("CL-{id}"),
            ..Default::default()
        }
    }

    async fn send_order(
        book: &mpsc::Sender<SymbolMsg>,
        order: fxp::NewOrderSingle,
        out: &OutputTx,
    ) {
        book.send(SymbolMsg::Order(OrderMsg {
            order,
            output_tx: out.clone(),
        }))
        .await
        .unwrap();
    }

    async fn next(rx: &mut Rx) -> fxp::ExecutionReport {
        let frame = tokio::time::timeout(Duration::from_secs(2), rx.recv())
            .await
            .expect("timed out waiting for report")
            .expect("channel closed");
        match fxp::FxpMessage::decode(&frame[4..]).unwrap().payload {
            Some(fxp::fxp_message::Payload::ExecutionReport(r)) => r,
            other => panic!("expected ExecutionReport, got {:?}", other),
        }
    }

    async fn assert_quiet(rx: &mut Rx) {
        tokio::time::sleep(Duration::from_millis(50)).await;
        assert!(rx.try_recv().is_err(), "unexpected extra report");
    }

    const BUY: i32 = 1;
    const SELL: i32 = 2;
    const MARKET: i32 = 1;
    const LIMIT: i32 = 2;
    const STOP: i32 = 3;
    const DAY: i32 = 1;
    const GTC: i32 = 2;
    const IOC: i32 = 3;

    fn et(r: &fxp::ExecutionReport) -> fxp::ExecutionType {
        fxp::ExecutionType::try_from(r.execution_type).unwrap()
    }
    fn st(r: &fxp::ExecutionReport) -> fxp::OrderStatus {
        fxp::OrderStatus::try_from(r.order_status).unwrap()
    }

    #[tokio::test]
    async fn fill_for_disconnected_owner_does_not_block() {
        let book = spawn_book();
        let (a, a_rx) = conn();
        let (b, mut b_rx) = conn();
        send_order(&book, nos("S1", "A", SELL, 100_00, 5, LIMIT, GTC), &a).await;
        tokio::time::sleep(Duration::from_millis(20)).await;
        drop(a_rx);
        send_order(&book, nos("B1", "B", BUY, 100_00, 5, LIMIT, DAY), &b).await;
        assert_eq!(et(&next(&mut b_rx).await), fxp::ExecutionType::ExecNew);
        assert_eq!(st(&next(&mut b_rx).await), fxp::OrderStatus::OrderFilled);
    }

    #[tokio::test]
    async fn resting_limit_gets_new_ack() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        send_order(&book, nos("A1", "A", BUY, 100_00, 10, LIMIT, GTC), &a).await;
        let r = next(&mut a_rx).await;
        assert_eq!(
            (et(&r), st(&r)),
            (fxp::ExecutionType::ExecNew, fxp::OrderStatus::OrderNew)
        );
        assert_eq!(
            (
                r.order_id.as_str(),
                r.leaves_quantity,
                r.cum_quantity,
                r.avg_px
            ),
            ("A1", 10, 0, 0)
        );
        assert_eq!(
            (r.client_order_id.as_str(), r.account.as_str()),
            ("CL-A1", "ACC-A")
        );
        assert!(!r.execution_id.is_empty());
        assert_eq!((r.side, r.order_quantity), (fxp::OrderSide::Buy as i32, 10));
        assert_quiet(&mut a_rx).await;
    }

    #[tokio::test]
    async fn partial_then_full_fill_reports_and_counterparty_routing() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        let (b, mut b_rx) = conn();
        let mut exec_ids = HashSet::new();

        send_order(&book, nos("S1", "A", SELL, 100_00, 10, LIMIT, GTC), &a).await;
        exec_ids.insert(next(&mut a_rx).await.execution_id);

        send_order(&book, nos("B1", "B", BUY, 101_00, 4, LIMIT, DAY), &b).await;
        let ack = next(&mut b_rx).await;
        assert_eq!(et(&ack), fxp::ExecutionType::ExecNew);
        exec_ids.insert(ack.execution_id);

        // Resting seller's report goes to the seller's own connection.
        let s = next(&mut a_rx).await;
        assert_eq!(
            (et(&s), st(&s)),
            (
                fxp::ExecutionType::ExecFilled,
                fxp::OrderStatus::OrderPartiallyFilled
            )
        );
        assert_eq!(
            (s.order_id.as_str(), s.execution_price, s.executed_quantity),
            ("S1", 100_00, 4)
        );
        assert_eq!(
            (s.cum_quantity, s.leaves_quantity, s.avg_px),
            (4, 6, 100_00)
        );
        assert_eq!(
            (s.client_order_id.as_str(), s.account.as_str()),
            ("CL-S1", "ACC-A")
        );
        assert_eq!(
            (s.side, s.order_quantity),
            (fxp::OrderSide::Sell as i32, 10)
        );
        exec_ids.insert(s.execution_id);

        let bf = next(&mut b_rx).await;
        assert_eq!(
            (et(&bf), st(&bf)),
            (
                fxp::ExecutionType::ExecFilled,
                fxp::OrderStatus::OrderFilled
            )
        );
        assert_eq!(
            (
                bf.order_id.as_str(),
                bf.execution_price,
                bf.executed_quantity
            ),
            ("B1", 100_00, 4)
        );
        assert_eq!(
            (bf.cum_quantity, bf.leaves_quantity, bf.avg_px),
            (4, 0, 100_00)
        );
        assert_eq!(
            (bf.client_order_id.as_str(), bf.account.as_str()),
            ("CL-B1", "ACC-B")
        );
        exec_ids.insert(bf.execution_id);

        // Second fill at the same price completes the seller.
        send_order(&book, nos("B2", "B", BUY, 100_00, 6, LIMIT, DAY), &b).await;
        exec_ids.insert(next(&mut b_rx).await.execution_id);
        let s2 = next(&mut a_rx).await;
        assert_eq!(st(&s2), fxp::OrderStatus::OrderFilled);
        assert_eq!(
            (
                s2.executed_quantity,
                s2.cum_quantity,
                s2.leaves_quantity,
                s2.avg_px
            ),
            (6, 10, 0, 100_00)
        );
        exec_ids.insert(s2.execution_id);
        exec_ids.insert(next(&mut b_rx).await.execution_id);

        assert_eq!(
            exec_ids.len(),
            7,
            "execution ids must be unique, including same-price fills"
        );
        assert_quiet(&mut a_rx).await;
        assert_quiet(&mut b_rx).await;
    }

    #[tokio::test]
    async fn multi_level_fill_reports_vwap() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        let (b, mut b_rx) = conn();
        send_order(&book, nos("S1", "A", SELL, 100_00, 5, LIMIT, GTC), &a).await;
        send_order(&book, nos("S2", "A", SELL, 102_00, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;
        next(&mut a_rx).await;

        send_order(&book, nos("M1", "B", BUY, 0, 10, MARKET, DAY), &b).await;
        let f1 = next(&mut b_rx).await;
        assert_eq!(
            (st(&f1), f1.cum_quantity, f1.leaves_quantity, f1.avg_px),
            (fxp::OrderStatus::OrderPartiallyFilled, 5, 5, 100_00)
        );
        let f2 = next(&mut b_rx).await;
        assert_eq!(
            (st(&f2), f2.cum_quantity, f2.leaves_quantity, f2.avg_px),
            (fxp::OrderStatus::OrderFilled, 10, 0, 101_00)
        );
        assert_ne!(f1.execution_id, f2.execution_id);
        assert_quiet(&mut b_rx).await;
    }

    #[tokio::test]
    async fn ioc_fill_reported_before_remainder_cancel() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        let (b, mut b_rx) = conn();
        send_order(&book, nos("S1", "A", SELL, 100_00, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;

        send_order(&book, nos("I1", "B", BUY, 100_00, 8, LIMIT, IOC), &b).await;
        let fill = next(&mut b_rx).await;
        assert_eq!(
            (et(&fill), st(&fill)),
            (
                fxp::ExecutionType::ExecFilled,
                fxp::OrderStatus::OrderPartiallyFilled
            )
        );
        assert_eq!(
            (
                fill.executed_quantity,
                fill.cum_quantity,
                fill.leaves_quantity,
                fill.avg_px
            ),
            (5, 5, 3, 100_00)
        );
        let cxl = next(&mut b_rx).await;
        assert_eq!(
            (et(&cxl), st(&cxl)),
            (
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled
            )
        );
        assert_eq!(
            (
                cxl.executed_quantity,
                cxl.cum_quantity,
                cxl.leaves_quantity,
                cxl.avg_px
            ),
            (0, 5, 0, 100_00)
        );
        assert_eq!(cxl.client_order_id, "CL-I1");
        assert_quiet(&mut b_rx).await;
    }

    #[tokio::test]
    async fn cancel_report_fields() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        send_order(&book, nos("A1", "A", BUY, 99_00, 10, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;
        book.send(SymbolMsg::Cancel(CancelMsg {
            order_id: "A1".into(),
            sender: "A".into(),
            client_cancel_id: "CXL-1".into(),
            output_tx: a.clone(),
        }))
        .await
        .unwrap();
        let r = next(&mut a_rx).await;
        assert_eq!(
            (et(&r), st(&r)),
            (
                fxp::ExecutionType::ExecCanceled,
                fxp::OrderStatus::OrderCanceled
            )
        );
        assert_eq!(
            (
                r.order_id.as_str(),
                r.client_order_id.as_str(),
                r.orig_order_id.as_str()
            ),
            ("A1", "CXL-1", "A1")
        );
        assert_eq!(
            (r.leaves_quantity, r.cum_quantity, r.account.as_str()),
            (0, 0, "ACC-A")
        );
    }

    #[tokio::test]
    async fn reject_report_fields() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        book.send(SymbolMsg::Cancel(CancelMsg {
            order_id: "NOPE".into(),
            sender: "A".into(),
            client_cancel_id: "CXL-9".into(),
            output_tx: a.clone(),
        }))
        .await
        .unwrap();
        let r = next(&mut a_rx).await;
        assert_eq!(
            (et(&r), st(&r)),
            (
                fxp::ExecutionType::ExecRejected,
                fxp::OrderStatus::OrderRejected
            )
        );
        assert_eq!(
            (
                r.order_id.as_str(),
                r.client_order_id.as_str(),
                r.orig_order_id.as_str()
            ),
            ("NOPE", "CXL-9", "NOPE")
        );
        assert!(!r.reject_reason.is_empty());

        // Order-level reject echoes the order's identifiers and is not a request reject.
        send_order(&book, nos("ST1", "A", SELL, 0, 5, STOP, DAY), &a).await;
        let r = next(&mut a_rx).await;
        assert_eq!(st(&r), fxp::OrderStatus::OrderRejected);
        assert_eq!(
            (
                r.order_id.as_str(),
                r.client_order_id.as_str(),
                r.account.as_str(),
                r.orig_order_id.as_str()
            ),
            ("ST1", "CL-ST1", "ACC-A", "")
        );
        assert!(!r.reject_reason.is_empty());
    }

    #[tokio::test]
    async fn invalid_quantity_and_price_are_rejected() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        let mut stop_limit = nos("SL1", "A", SELL, 0, 5, 4, GTC);
        stop_limit.stop_price = 99_00;
        for order in [
            nos("Q0", "A", BUY, 100_00, 0, LIMIT, GTC),
            nos("QN", "A", BUY, 100_00, -5, LIMIT, GTC),
            nos("QM", "A", BUY, 0, -1, MARKET, DAY),
            nos("P0", "A", BUY, 0, 10, LIMIT, GTC),
            nos("PN", "A", SELL, -100, 10, LIMIT, GTC),
            stop_limit,
        ] {
            let id = order.order_id.clone();
            send_order(&book, order, &a).await;
            let r = next(&mut a_rx).await;
            assert_eq!(
                (r.order_id.as_str(), st(&r)),
                (id.as_str(), fxp::OrderStatus::OrderRejected)
            );
            assert!(!r.reject_reason.is_empty());
            assert_eq!(
                (r.orig_order_id.as_str(), r.leaves_quantity, r.cum_quantity),
                ("", 0, 0)
            );
        }
        // Nothing rested: a market sell finds no liquidity and is cancelled.
        send_order(&book, nos("M1", "A", SELL, 0, 1, MARKET, DAY), &a).await;
        assert_eq!(st(&next(&mut a_rx).await), fxp::OrderStatus::OrderCanceled);
        assert_quiet(&mut a_rx).await;
    }

    #[tokio::test]
    async fn unspecified_and_unknown_order_enums_are_rejected() {
        let book = spawn_book();
        let (out, mut rx) = conn();
        for (id, side, order_type, time_in_force, reason) in [
            ("S0", 0, LIMIT, GTC, "side is required"),
            ("S99", 99, LIMIT, GTC, "unknown side 99"),
            ("T0", BUY, 0, GTC, "order_type is required"),
            ("T99", BUY, 99, GTC, "unknown order_type 99"),
            ("F0", BUY, LIMIT, 0, "time_in_force is required"),
            ("F99", BUY, LIMIT, 99, "unknown time_in_force 99"),
        ] {
            send_order(
                &book,
                nos(id, "A", side, 100_00, 10, order_type, time_in_force),
                &out,
            )
            .await;
            let report = next(&mut rx).await;
            assert_eq!(et(&report), fxp::ExecutionType::ExecRejected, "{id}");
            assert_eq!(st(&report), fxp::OrderStatus::OrderRejected, "{id}");
            assert_eq!(report.reject_reason, reason, "{id}");
        }
    }

    #[tokio::test]
    async fn unknown_replace_time_in_force_is_rejected() {
        let book = spawn_book();
        let (out, mut rx) = conn();
        send_order(&book, nos("R0", "A", BUY, 100_00, 10, LIMIT, GTC), &out).await;
        assert_eq!(et(&next(&mut rx).await), fxp::ExecutionType::ExecNew);

        book.send(SymbolMsg::CancelReplace(CancelReplaceMsg {
            order_id: "R0".into(),
            sender: "A".into(),
            new_order_id: "R1".into(),
            client_replace_id: "CR1".into(),
            new_price: 0,
            new_quantity: 0,
            new_time_in_force: 99,
            new_stop_price: 0,
            output_tx: out,
        }))
        .await
        .unwrap();
        let report = next(&mut rx).await;
        assert_eq!(et(&report), fxp::ExecutionType::ExecRejected);
        assert_eq!(report.reject_reason, "unknown new_time_in_force 99");
    }

    #[tokio::test]
    async fn unspecified_replace_time_in_force_keeps_existing_value() {
        let book = spawn_book();
        let (out, mut rx) = conn();
        send_order(&book, nos("P0", "A", BUY, 100_00, 10, LIMIT, GTC), &out).await;
        assert_eq!(et(&next(&mut rx).await), fxp::ExecutionType::ExecNew);

        book.send(SymbolMsg::CancelReplace(CancelReplaceMsg {
            order_id: "P0".into(),
            sender: "A".into(),
            new_order_id: "P1".into(),
            client_replace_id: "CR2".into(),
            new_price: 0,
            new_quantity: 0,
            new_time_in_force: 0,
            new_stop_price: 0,
            output_tx: out,
        }))
        .await
        .unwrap();
        let original = next(&mut rx).await;
        let replacement = next(&mut rx).await;
        assert_eq!(et(&original), fxp::ExecutionType::ExecReplaced);
        assert_eq!(et(&replacement), fxp::ExecutionType::ExecNew);
        assert_eq!(replacement.order_id, "P1");

        let mut standalone = crate::test_helpers::make_book();
        standalone.add_order(crate::test_helpers::make_order(
            "P0",
            crate::order_book::order::OrderSide::Buy,
            100_00,
            10,
            crate::order_book::order::OrderType::Limit,
            crate::order_book::order::TimeInForce::GTC,
        ));
        let (kept, _) = standalone
            .replace_order("P0", "P1", 0, 0, None, "CR2", "T", 1)
            .unwrap();
        assert_eq!(
            kept.time_in_force,
            crate::order_book::order::TimeInForce::GTC
        );
    }

    #[test]
    fn accept_backoff_doubles_and_caps() {
        let mut d = ACCEPT_BACKOFF_MIN;
        let mut seen = vec![d];
        for _ in 0..12 {
            d = next_accept_backoff(d);
            seen.push(d);
        }
        assert_eq!(seen[1], ACCEPT_BACKOFF_MIN * 2);
        assert!(seen.windows(2).all(|w| w[1] >= w[0]));
        assert_eq!(*seen.last().unwrap(), ACCEPT_BACKOFF_MAX);
    }

    // ── Stop orders ──────────────────────────────────────────────────────────

    const STOP_LIMIT: i32 = 4;
    const FOK: i32 = 4;

    fn stop(id: &str, trader: &str, side: i32, stop_px: i64, qty: i32) -> fxp::NewOrderSingle {
        fxp::NewOrderSingle {
            stop_price: stop_px,
            ..nos(id, trader, side, 0, qty, STOP, DAY)
        }
    }

    async fn rest(
        book: &mpsc::Sender<SymbolMsg>,
        out: &OutputTx,
        rx: &mut Rx,
        o: fxp::NewOrderSingle,
    ) {
        send_order(book, o, out).await;
        assert_eq!(et(&next(rx).await), fxp::ExecutionType::ExecNew);
    }

    /// Next `n` fill reports as (order_id, execution_price), skipping other reports.
    async fn fills(rx: &mut Rx, n: usize) -> Vec<(String, i64)> {
        let mut v = Vec::new();
        while v.len() < n {
            let r = next(rx).await;
            if et(&r) == fxp::ExecutionType::ExecFilled {
                v.push((r.order_id, r.execution_price));
            }
        }
        v
    }

    #[tokio::test]
    async fn stop_fires_on_intermediate_print_of_sweep() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MB1", "M", BUY, 102_00, 5, LIMIT, GTC),
        )
        .await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MB2", "M", BUY, 100_00, 5, LIMIT, GTC),
        )
        .await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA1", "M", SELL, 105_00, 5, LIMIT, GTC),
        )
        .await;
        rest(&book, &t, &mut t_rx, stop("S", "T", BUY, 102_00, 2)).await;
        // Sell sweep prints 102 then 100: the 102 print must trigger the buy stop.
        send_order(&book, nos("AG", "A", SELL, 0, 10, MARKET, DAY), &conn().0).await;
        assert_eq!(fills(&mut t_rx, 1).await, vec![("S".to_string(), 105_00)]);
    }

    #[tokio::test]
    async fn triggered_stops_fire_by_stop_price_then_arrival() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        for (id, px) in [
            ("MA4", 104_00),
            ("MA5", 105_00),
            ("MA6", 106_00),
            ("MA7", 107_00),
        ] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        for (id, px) in [
            ("S103", 103_00),
            ("S101", 101_00),
            ("S102a", 102_00),
            ("S102b", 102_00),
        ] {
            rest(&book, &t, &mut t_rx, stop(id, "T", BUY, px, 1)).await;
        }
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA3", "M", SELL, 103_00, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 103_00, 1, LIMIT, IOC), &conn().0).await;
        let want = [
            ("S101", 104_00),
            ("S102a", 105_00),
            ("S102b", 106_00),
            ("S103", 107_00),
        ];
        let want: Vec<_> = want.iter().map(|(i, p)| (i.to_string(), *p)).collect();
        assert_eq!(fills(&mut t_rx, 4).await, want);
    }

    #[tokio::test]
    async fn three_stop_cascade_orders_reports_and_last_trade_price() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        for (id, px) in [
            ("MA104", 104_00),
            ("MA105", 105_00),
            ("MA106", 106_00),
            ("MA107", 107_00),
        ] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        rest(&book, &t, &mut t_rx, stop("S1", "T", BUY, 101_00, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S2", "T", BUY, 104_00, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S3", "T", BUY, 105_00, 1)).await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA101", "M", SELL, 101_00, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 101_00, 1, LIMIT, IOC), &conn().0).await;
        let want: Vec<_> = [("S1", 104_00), ("S2", 105_00), ("S3", 106_00)]
            .iter()
            .map(|(i, p)| (i.to_string(), *p))
            .collect();
        assert_eq!(fills(&mut t_rx, 3).await, want);
        // last_trade_price is now 106: a buy stop at 106 fires on arrival, one at 108 parks.
        send_order(&book, stop("S4", "T", BUY, 106_00, 1), &t).await;
        assert_eq!(fills(&mut t_rx, 1).await, vec![("S4".to_string(), 107_00)]);
        send_order(&book, stop("S5", "T", BUY, 108_00, 1), &t).await;
        let r = next(&mut t_rx).await;
        assert_eq!(
            (r.order_id.as_str(), et(&r)),
            ("S5", fxp::ExecutionType::ExecNew)
        );
    }

    #[tokio::test]
    async fn sell_stop_parks_before_first_trade() {
        let book = spawn_book();
        let (t, mut t_rx) = conn();
        rest(&book, &t, &mut t_rx, stop("S", "T", SELL, 100_00, 1)).await;
        assert_quiet(&mut t_rx).await;
    }

    #[tokio::test]
    async fn parked_stop_can_be_cancelled_only_by_owner() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        let (x, mut x_rx) = conn();
        rest(&book, &t, &mut t_rx, stop("S", "T", BUY, 100_00, 1)).await;
        for (sender, out) in [("X", &x), ("T", &t)] {
            book.send(SymbolMsg::Cancel(CancelMsg {
                order_id: "S".into(),
                sender: sender.into(),
                client_cancel_id: format!("C-{sender}"),
                output_tx: out.clone(),
            }))
            .await
            .unwrap();
        }
        assert_eq!(et(&next(&mut x_rx).await), fxp::ExecutionType::ExecRejected);
        let r = next(&mut t_rx).await;
        assert_eq!(
            (r.order_id.as_str(), et(&r)),
            ("S", fxp::ExecutionType::ExecCanceled)
        );
        // A trigger print afterwards must not execute the cancelled stop.
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA", "M", SELL, 100_00, 2, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 100_00, 1, LIMIT, IOC), &conn().0).await;
        assert_quiet(&mut t_rx).await;
    }

    async fn stop_limit_tif_after_trigger(tif: i32) -> Vec<fxp::ExecutionType> {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA101", "M", SELL, 101_00, 2, LIMIT, GTC),
        )
        .await;
        let sl = fxp::NewOrderSingle {
            stop_price: 100_00,
            ..nos("SL", "T", BUY, 101_00, 5, STOP_LIMIT, tif)
        };
        rest(&book, &t, &mut t_rx, sl).await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA100", "M", SELL, 100_00, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 100_00, 1, LIMIT, IOC), &conn().0).await;
        let mut seen = Vec::new();
        while let Ok(Some(frame)) =
            tokio::time::timeout(Duration::from_millis(200), t_rx.recv()).await
        {
            if let Some(fxp::fxp_message::Payload::ExecutionReport(r)) =
                fxp::FxpMessage::decode(&frame[4..]).unwrap().payload
            {
                seen.push(et(&r));
            }
        }
        // Nothing of T's may rest: a sell at 99 must rest rather than fill against it.
        let (q, mut q_rx) = conn();
        send_order(&book, nos("MS", "Q", SELL, 99_00, 1, LIMIT, GTC), &q).await;
        assert_eq!(et(&next(&mut q_rx).await), fxp::ExecutionType::ExecNew);
        assert_quiet(&mut q_rx).await;
        seen
    }

    #[tokio::test]
    async fn stop_limit_ioc_cancels_remainder_after_trigger() {
        let seen = stop_limit_tif_after_trigger(IOC).await;
        assert_eq!(
            seen,
            vec![
                fxp::ExecutionType::ExecFilled,
                fxp::ExecutionType::ExecCanceled
            ]
        );
    }

    #[tokio::test]
    async fn stop_limit_fok_is_killed_after_trigger_without_liquidity() {
        let seen = stop_limit_tif_after_trigger(FOK).await;
        assert_eq!(seen, vec![fxp::ExecutionType::ExecCanceled]);
    }

    #[tokio::test]
    async fn replacing_parked_stop_price_reorders_triggers() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        let (x, mut x_rx) = conn();
        for (id, px) in [("MA110", 110_00), ("MA111", 111_00)] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        rest(&book, &t, &mut t_rx, stop("S1", "T", BUY, 105_00, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S2", "T", BUY, 103_00, 1)).await;
        let replace = |sender: &str, out: &OutputTx| {
            SymbolMsg::CancelReplace(CancelReplaceMsg {
                order_id: "S1".into(),
                sender: sender.into(),
                new_order_id: "S1R".into(),
                client_replace_id: format!("CR-{sender}"),
                new_price: 0,
                new_quantity: 0,
                new_time_in_force: 0,
                new_stop_price: 101_00,
                output_tx: out.clone(),
            })
        };
        book.send(replace("X", &x)).await.unwrap();
        assert_eq!(et(&next(&mut x_rx).await), fxp::ExecutionType::ExecRejected);
        book.send(replace("T", &t)).await.unwrap();
        let (r1, r2) = (next(&mut t_rx).await, next(&mut t_rx).await);
        assert_eq!(
            [
                (r1.order_id.as_str(), et(&r1)),
                (r2.order_id.as_str(), et(&r2))
            ],
            [
                ("S1", fxp::ExecutionType::ExecReplaced),
                ("S1R", fxp::ExecutionType::ExecNew)
            ]
        );
        // A 103 print triggers both; S1R (stop 101) now fires before S2 (stop 103).
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA103", "M", SELL, 103_00, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 103_00, 1, LIMIT, IOC), &conn().0).await;
        let want: Vec<_> = [("S1R", 110_00), ("S2", 111_00)]
            .iter()
            .map(|(i, p)| (i.to_string(), *p))
            .collect();
        assert_eq!(fills(&mut t_rx, 2).await, want);
    }
}

#[cfg(test)]
mod snapshot_tests {
    use super::*;

    #[test]
    fn snapshot_frame_carries_every_level_best_first() {
        let mut book = OrderBook::new("SNAP", Arc::new(MarketDataBroadcaster::new()));
        for (id, px, qty, side) in [
            ("b1", 990, 20, OrderSide::Buy),
            ("b2", 1000, 10, OrderSide::Buy),
            ("a1", 1020, 40, OrderSide::Sell),
            ("a2", 1010, 30, OrderSide::Sell),
        ] {
            book.add_order(Order::simple(
                id.into(),
                "T".into(),
                "SNAP".into(),
                px,
                qty,
                side,
                OrderType::Limit,
                TimeInForce::GTC,
                0,
            ));
        }
        let frame = snapshot_frame(&book);
        let Some(fxp::fxp_message::Payload::MarketDataSnapshot(snap)) =
            fxp::FxpMessage::decode(&frame[4..]).unwrap().payload
        else {
            panic!("not a snapshot");
        };
        let lv = |v: &[fxp::MarketOrderBookEntry]| {
            v.iter()
                .map(|e| (e.price, e.size, e.side))
                .collect::<Vec<_>>()
        };
        let (b, s) = (fxp::OrderSide::Buy as i32, fxp::OrderSide::Sell as i32);
        assert_eq!(lv(&snap.bid_levels), vec![(1000, 10, b), (990, 20, b)]);
        assert_eq!(lv(&snap.ask_levels), vec![(1010, 30, s), (1020, 40, s)]);
    }
}
