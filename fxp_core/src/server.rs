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
use crate::order_book::market_data::MarketUpdateType;
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::OrderBook;
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
    let mut stop_book: Vec<crate::order_book::order::Order> = Vec::new();
    let mut last_trade_price: i64 = 0; // 0 = no trade yet this session
    // Report state of every live order (resting, parked stop or in-flight)
    let mut states: HashMap<String, OrderState> = HashMap::new();

    println!("📚 Order book task started for {}", symbol);

    while let Some(msg) = rx.recv().await {
        match msg {
            SymbolMsg::Subscribe(sub) => {
                subscribers.push(sub.tx);
                println!("✅ Client subscribed to {}", symbol);
            }

            SymbolMsg::Cancel(cancel_msg) => {
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                let ts = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos: now.subsec_nanos() as i32,
                });

                match book.cancel_order_for(&cancel_msg.order_id, &cancel_msg.sender) {
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

                        // Broadcast cancel incremental to market data subscribers
                        let md_frame = frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                fxp::MarketDataIncrementalUpdate {
                                    symbol: symbol.clone(),
                                    header: Some(fxp::FxpHeader {
                                        protocol_version: crate::PROTOCOL_VERSION.to_string(),
                                        sequence_number: 1,
                                    }),
                                    timestamp: ts.clone(),
                                    changes: md_update
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
                                        })
                                        .collect(),
                                },
                            )),
                        });
                        broadcast_md(&mut subscribers, md_frame).await;
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
                        let leaves = new_state.leaves();

                        // EXEC_REPLACED for the original order, EXEC_NEW for the replacement
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
                                .send(build_report(id, &symbol, Some(&new_state), ev, &ts))
                                .await;
                        }
                        states.insert(cr_msg.new_order_id.clone(), new_state);

                        // Send any immediate fills from the replacement
                        let (passive_fills, fill_updates) = book.match_orders();
                        for exec in &passive_fills {
                            for id in [&exec.buy_order_id, &exec.sell_order_id] {
                                if let Some((frame, tx)) = apply_fill(
                                    &mut states,
                                    id,
                                    &symbol,
                                    exec.trade_price,
                                    exec.trade_quantity,
                                    &ts,
                                ) {
                                    let _ = tx.send(frame).await;
                                }
                            }
                        }

                        // Broadcast incremental updates
                        let md_frame = frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                fxp::MarketDataIncrementalUpdate {
                                    symbol: symbol.clone(),
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
                                        })
                                        .collect(),
                                },
                            )),
                        });
                        broadcast_md(&mut subscribers, md_frame).await;

                        for fill in fill_updates {
                            let fill_frame = frame_message(&fxp::FxpMessage {
                                payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                    fxp::MarketDataIncrementalUpdate {
                                        symbol: fill.symbol.clone(),
                                        header: Some(fxp::FxpHeader {
                                            protocol_version: crate::PROTOCOL_VERSION.to_string(),
                                            sequence_number: 1,
                                        }),
                                        timestamp: ts.clone(),
                                        changes: fill
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
                                            })
                                            .collect(),
                                    },
                                )),
                            });
                            broadcast_md(&mut subscribers, fill_frame).await;
                        }

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

                    let fires_immediately = last_trade_price > 0
                        && match order_obj.side {
                            OrderSide::Buy => last_trade_price >= order_obj.stop_price,
                            OrderSide::Sell => last_trade_price <= order_obj.stop_price,
                        };

                    if fires_immediately {
                        // Trigger immediately — fall through to normal processing below
                        println!(
                            "⚡ Stop {} triggered immediately at last_trade={}",
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
                            "🛑 Stop {} parked on {} (stop={} last_trade={})",
                            order.order_id, symbol, order_obj.stop_price, last_trade_price
                        );
                        states.insert(order_obj.order_id.clone(), state);
                        stop_book.push(order_obj);
                        continue;
                    }
                }

                // Process the order (non-stop, or stop that triggered immediately)
                states.insert(order_obj.order_id.clone(), state);
                let aggressive = book.process_aggressive(order_obj.clone());
                // Market/IOC/FOK remainders, cancelled after their fills are reported
                let mut pending_cancels: Vec<String> = Vec::new();

                let mut all_fills = aggressive.fills;
                let mut all_fill_updates = aggressive.fill_updates;
                let mut incremental_opt = None;

                if aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
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
                    let mut resting = order_obj.clone();
                    resting.quantity = aggressive.unfilled_qty;
                    let inc = book.add_order(resting);
                    incremental_opt = Some(inc);
                    let (passive_fills, passive_updates) = book.match_orders();
                    all_fills.extend(passive_fills);
                    all_fill_updates.extend(passive_updates);
                } else if !aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
                    pending_cancels.push(order.order_id.clone());
                }

                // Update last_trade_price from fills and check stop triggers.
                // Loop handles cascading stops (a triggered stop may itself cause
                // fills that trigger more stops).
                if !all_fills.is_empty() {
                    // Use the last fill price as the new last_trade_price
                    last_trade_price = all_fills.last().unwrap().trade_price;

                    // Check stop triggers — may produce additional fills
                    loop {
                        let triggered =
                            OrderBook::drain_triggered_stops(&mut stop_book, last_trade_price);
                        if triggered.is_empty() {
                            break;
                        }

                        println!(
                            "⚡ {} stop(s) triggered at price {}",
                            triggered.len(),
                            last_trade_price
                        );

                        for stop_order in triggered {
                            let stop_result = book.process_aggressive(stop_order.clone());
                            // Save last fill price before consuming the vecs
                            let stop_last_price = stop_result.fills.last().map(|f| f.trade_price);
                            all_fills.extend(stop_result.fills.into_iter());
                            all_fill_updates.extend(stop_result.fill_updates.into_iter());

                            if stop_result.rest_on_book && stop_result.unfilled_qty > 0 {
                                // StopLimit triggered — add as resting limit
                                let mut resting = stop_order.clone();
                                resting.order_type = OrderType::Limit;
                                resting.quantity = stop_result.unfilled_qty;
                                let inc = book.add_order(resting);
                                // Broadcast the new resting limit's incremental
                                let md = frame_message(&fxp::FxpMessage {
                                    payload: Some(
                                        fxp::fxp_message::Payload::MarketDataIncremental(
                                            fxp::MarketDataIncrementalUpdate {
                                                symbol: symbol.clone(),
                                                header: Some(fxp::FxpHeader {
                                                    protocol_version: crate::PROTOCOL_VERSION
                                                        .to_string(),
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
                                                    })
                                                    .collect(),
                                            },
                                        ),
                                    ),
                                });
                                broadcast_md(&mut subscribers, md).await;
                                let (extra_fills, extra_updates) = book.match_orders();
                                all_fills.extend(extra_fills.into_iter());
                                all_fill_updates.extend(extra_updates);
                            } else if stop_result.unfilled_qty > 0 {
                                pending_cancels.push(stop_order.order_id.clone());
                            }

                            // Update last_trade_price if stop produced fills
                            if let Some(p) = stop_last_price {
                                last_trade_price = p;
                            }
                        }
                    }
                }

                // Fill reports go to each order's own connection; remainder
                // cancels follow the fills they complete.
                for exec in &all_fills {
                    for id in [&exec.buy_order_id, &exec.sell_order_id] {
                        if let Some((frame, tx)) = apply_fill(
                            &mut states,
                            id,
                            &symbol,
                            exec.trade_price,
                            exec.trade_quantity,
                            &ts,
                        ) {
                            let _ = tx.send(frame).await;
                        }
                    }
                }
                for id in pending_cancels {
                    if let Some(st) = states.remove(&id) {
                        let ev = ReportEvent::new(
                            fxp::ExecutionType::ExecCanceled,
                            fxp::OrderStatus::OrderCanceled,
                        );
                        let _ = st
                            .output_tx
                            .send(build_report(&id, &symbol, Some(&st), ev, &ts))
                            .await;
                    }
                }

                // Broadcast add-order incremental (only for orders that rested on the book)
                if let Some(incremental) = incremental_opt {
                    let md_add_frame = frame_message(&fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                            fxp::MarketDataIncrementalUpdate {
                                symbol: symbol.clone(),
                                header: Some(fxp::FxpHeader {
                                    protocol_version: crate::PROTOCOL_VERSION.to_string(),
                                    sequence_number: 1,
                                }),
                                timestamp: ts.clone(),
                                changes: incremental
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
                                    })
                                    .collect(),
                            },
                        )),
                    });
                    broadcast_md(&mut subscribers, md_add_frame).await;
                }

                // Broadcast fill incremental updates
                for fill in all_fill_updates {
                    let md_fill_frame = frame_message(&fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                            fxp::MarketDataIncrementalUpdate {
                                symbol: fill.symbol.clone(),
                                header: Some(fxp::FxpHeader {
                                    protocol_version: crate::PROTOCOL_VERSION.to_string(),
                                    sequence_number: 1,
                                }),
                                timestamp: ts.clone(),
                                changes: fill
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
                                    })
                                    .collect(),
                            },
                        )),
                    });
                    broadcast_md(&mut subscribers, md_fill_frame).await;
                }
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
}
