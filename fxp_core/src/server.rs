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

use tokio::net::TcpListener;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::mpsc;
use prost::Message;
use std::sync::Arc;
use std::collections::HashMap;
use tokio::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::net::tcp::OwnedReadHalf;

mod fxp {
    include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
}

use prost_types::Timestamp;
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::OrderBook;
use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::MarketUpdateType;

// ── Output channel ────────────────────────────────────────────────────────────
// Each connection gets one output channel. All tasks that need to write to
// this connection send frames here. The write_task drains it exclusively.
type OutputTx = mpsc::Sender<Vec<u8>>;

// ── Messages dispatched to a symbol's task ────────────────────────────────────

struct OrderMsg {
    order:     fxp::NewOrderSingle,
    output_tx: OutputTx,
}

struct SubscribeMsg {
    tx: mpsc::Sender<Vec<u8>>,
}

struct CancelMsg {
    order_id:         String,
    client_cancel_id: String,
    output_tx:        OutputTx,
}

struct CancelReplaceMsg {
    order_id:          String,
    new_order_id:      String,
    client_replace_id: String,
    new_price:         i64,   // 0 = no change
    new_quantity:      i32,   // 0 = no change
    output_tx:         OutputTx,
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
        Self { channels: Arc::new(Mutex::new(HashMap::new())) }
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
        tx  // return cloned sender directly — no need to re-acquire
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

    println!("📚 Order book task started for {}", symbol);

    while let Some(msg) = rx.recv().await {
        match msg {
            SymbolMsg::Subscribe(sub) => {
                subscribers.push(sub.tx);
                println!("✅ Client subscribed to {}", symbol);
            }

            SymbolMsg::Cancel(cancel_msg) => {
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                let ts  = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos:   now.subsec_nanos() as i32,
                });

                match book.cancel_order(&cancel_msg.order_id) {
                    Some(md_update) => {
                        // Order found and cancelled — send EXEC_CANCELED report
                        let report = make_exec_report(
                            &cancel_msg.order_id,
                            &symbol,
                            &cancel_msg.client_cancel_id,
                            fxp::ExecutionType::ExecCanceled,
                            fxp::OrderStatus::OrderCanceled,
                            0, 0, 0, 0, 0,
                            &ts,
                        );
                        let _ = cancel_msg.output_tx.send(report).await;

                        // Broadcast cancel incremental to market data subscribers
                        let md_frame = frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                fxp::MarketDataIncrementalUpdate {
                                    symbol:    symbol.clone(),
                                    header:    Some(fxp::FxpHeader { protocol_version: "1.1".to_string(), sequence_number: 1 }),
                                    timestamp: ts.clone(),
                                    changes:   md_update.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                        update_type: match c.update_type {
                                            MarketUpdateType::NewOrder    => 0,
                                            MarketUpdateType::ModifyOrder => 1,
                                            MarketUpdateType::CancelOrder => 2,
                                        },
                                        price:    c.price,
                                        quantity: c.quantity as i32,
                                    }).collect(),
                                },
                            )),
                        });
                        broadcast_md(&mut subscribers, md_frame).await;
                        println!("🗑️ Cancelled order {} on {}", cancel_msg.order_id, symbol);
                    }
                    None => {
                        // Order not found — already filled, cancelled, or unknown
                        let report = make_exec_report(
                            &cancel_msg.order_id,
                            &symbol,
                            &cancel_msg.client_cancel_id,
                            fxp::ExecutionType::ExecRejected,
                            fxp::OrderStatus::OrderRejected,
                            0, 0, 0, 0, 0,
                            &ts,
                        );
                        let _ = cancel_msg.output_tx.send(report).await;
                        println!("⚠️ Cancel rejected: order {} not found on {}", cancel_msg.order_id, symbol);
                    }
                }
            }

            SymbolMsg::CancelReplace(cr_msg) => {
                let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                let ts  = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos:   now.subsec_nanos() as i32,
                });

                // Find original order to get current state
                let original = book.order_lookup.get(&cr_msg.order_id).cloned();

                match original {
                    None => {
                        // Order not found — reject
                        let report = make_exec_report(
                            &cr_msg.order_id, &symbol, &cr_msg.client_replace_id,
                            fxp::ExecutionType::ExecRejected,
                            fxp::OrderStatus::OrderRejected,
                            0, 0, 0, 0, 0, &ts,
                        );
                        let _ = cr_msg.output_tx.send(report).await;
                        println!("⚠️ CancelReplace rejected: order {} not found on {}", cr_msg.order_id, symbol);
                    }
                    Some(orig) => {
                        // Cancel the original
                        let _ = book.cancel_order(&cr_msg.order_id);

                        // Build replacement order with amended fields
                        let new_price    = if cr_msg.new_price    > 0 { cr_msg.new_price    } else { orig.price };
                        let new_quantity = if cr_msg.new_quantity  > 0 { cr_msg.new_quantity as u32 } else { orig.quantity };

                        let replacement = crate::order_book::order::Order {
                            order_id:       cr_msg.new_order_id.clone(),
                            trader_id:      orig.trader_id.clone(),
                            symbol:         symbol.clone(),
                            price:          new_price,
                            quantity:       new_quantity,
                            side:           orig.side,
                            order_type:     orig.order_type,
                            time_in_force:  orig.time_in_force,
                            timestamp:      now.as_millis() as u64,
                            stop_price:     orig.stop_price,
                            expire_date:    orig.expire_date.clone(),
                            account:        orig.account.clone(),
                            client_order_id: cr_msg.client_replace_id.clone(),
                        };

                        let inc = book.add_order(replacement);
                        let (passive_fills, fill_updates) = book.match_orders();

                        // Send EXEC_REPLACED for the original order
                        let replaced_report = make_exec_report(
                            &cr_msg.order_id, &symbol, &cr_msg.client_replace_id,
                            fxp::ExecutionType::ExecReplaced,
                            fxp::OrderStatus::OrderReplaced,
                            0, 0, new_quantity as i32, 0, 0, &ts,
                        );
                        let _ = cr_msg.output_tx.send(replaced_report).await;

                        // Send EXEC_NEW for the replacement order
                        let new_report = make_exec_report(
                            &cr_msg.new_order_id, &symbol, &cr_msg.client_replace_id,
                            fxp::ExecutionType::ExecNew,
                            fxp::OrderStatus::OrderNew,
                            0, 0, new_quantity as i32, new_quantity as i32, 0, &ts,
                        );
                        let _ = cr_msg.output_tx.send(new_report).await;

                        // Send any immediate fills from the replacement
                        for exec in &passive_fills {
                            let make_report = |order_id: String, fully_filled: bool, leaves: i32, cum: i32| -> Vec<u8> {
                                make_exec_report(
                                    &order_id, &symbol, "",
                                    fxp::ExecutionType::ExecFilled,
                                    if fully_filled { fxp::OrderStatus::OrderFilled } else { fxp::OrderStatus::OrderPartiallyFilled },
                                    exec.trade_price, exec.trade_quantity as i32, new_quantity as i32, leaves, cum, &ts,
                                )
                            };
                            let buy_leaves  = if exec.buy_fully_filled  { 0 } else { exec.trade_quantity as i32 };
                            let sell_leaves = if exec.sell_fully_filled { 0 } else { exec.trade_quantity as i32 };
                            let _ = cr_msg.output_tx.send(make_report(exec.buy_order_id.clone(),  exec.buy_fully_filled,  buy_leaves,  exec.trade_quantity as i32)).await;
                            let _ = cr_msg.output_tx.send(make_report(exec.sell_order_id.clone(), exec.sell_fully_filled, sell_leaves, exec.trade_quantity as i32)).await;
                        }

                        // Broadcast incremental updates
                        let md_frame = frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                fxp::MarketDataIncrementalUpdate {
                                    symbol:    symbol.clone(),
                                    header:    Some(fxp::FxpHeader { protocol_version: "1.1".to_string(), sequence_number: 1 }),
                                    timestamp: ts.clone(),
                                    changes:   inc.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                        update_type: match c.update_type {
                                            MarketUpdateType::NewOrder    => 0,
                                            MarketUpdateType::ModifyOrder => 1,
                                            MarketUpdateType::CancelOrder => 2,
                                        },
                                        price:    c.price,
                                        quantity: c.quantity as i32,
                                    }).collect(),
                                },
                            )),
                        });
                        broadcast_md(&mut subscribers, md_frame).await;

                        for fill in fill_updates {
                            let fill_frame = frame_message(&fxp::FxpMessage {
                                payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                    fxp::MarketDataIncrementalUpdate {
                                        symbol:    fill.symbol.clone(),
                                        header:    Some(fxp::FxpHeader { protocol_version: "1.1".to_string(), sequence_number: 1 }),
                                        timestamp: ts.clone(),
                                        changes:   fill.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                            update_type: match c.update_type {
                                                MarketUpdateType::NewOrder    => 0,
                                                MarketUpdateType::ModifyOrder => 1,
                                                MarketUpdateType::CancelOrder => 2,
                                            },
                                            price:    c.price,
                                            quantity: c.quantity as i32,
                                        }).collect(),
                                    },
                                )),
                            });
                            broadcast_md(&mut subscribers, fill_frame).await;
                        }

                        println!("🔄 CancelReplaced {} → {} on {}", cr_msg.order_id, cr_msg.new_order_id, symbol);
                    }
                }
            }

            SymbolMsg::Order(order_msg) => {
                let order     = order_msg.order;
                let output_tx = order_msg.output_tx;
                let now       = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();

                let order_obj = Order {
                    order_id:      order.order_id.clone(),
                    trader_id:     order.sender.clone(),
                    symbol:        symbol.clone(),
                    price:         order.price,
                    quantity:      order.quantity as u32,
                    side: match order.side {
                        0 => OrderSide::Buy,
                        _ => OrderSide::Sell,
                    },
                    order_type: match order.order_type {
                        1 => OrderType::Limit,
                        2 => OrderType::Stop,
                        3 => OrderType::StopLimit,
                        _ => OrderType::Market,
                    },
                    time_in_force: match order.time_in_force {
                        1 => TimeInForce::GTC,
                        2 => TimeInForce::IOC,
                        3 => TimeInForce::FOK,
                        4 => TimeInForce::AtOpen,
                        5 => TimeInForce::AtClose,
                        6 => TimeInForce::Gtd,
                        _ => TimeInForce::Day,
                    },
                    timestamp:        now.as_millis() as u64,
                    stop_price:       order.stop_price,
                    expire_date:      order.expire_date.clone(),
                    account:          order.account.clone(),
                    client_order_id:  order.client_order_id.clone(),
                };

                let seq = order.header.as_ref().map_or(0, |h| h.sequence_number + 1);
                let ts  = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos:   now.subsec_nanos() as i32,
                });

                // Stop and StopLimit orders rest in stop_book until triggered.
                // Check immediately in case last_trade_price already satisfies
                // the stop condition (e.g. order placed after a large move).
                if matches!(order_obj.order_type, OrderType::Stop | OrderType::StopLimit) {
                    if order_obj.stop_price == 0 {
                        // Reject — stop_price is required
                        let report = make_exec_report(
                            &order_obj.order_id, &symbol, "",
                            fxp::ExecutionType::ExecRejected,
                            fxp::OrderStatus::OrderRejected,
                            0, 0, 0, 0, 0, &ts,
                        );
                        let _ = output_tx.send(report).await;
                        println!("⚠️ Stop order {} rejected: stop_price is required", order_obj.order_id);
                        continue;
                    }

                    let fires_immediately = last_trade_price > 0 && match order_obj.side {
                        OrderSide::Buy  => last_trade_price >= order_obj.stop_price,
                        OrderSide::Sell => last_trade_price <= order_obj.stop_price,
                    };

                    if fires_immediately {
                        // Trigger immediately — fall through to normal processing below
                        println!("⚡ Stop {} triggered immediately at last_trade={}", order_obj.order_id, last_trade_price);
                    } else {
                        // Park in stop_book — send EXEC_NEW acknowledgement
                        let ack = make_exec_report(
                            &order_obj.order_id, &symbol, "",
                            fxp::ExecutionType::ExecNew,
                            fxp::OrderStatus::OrderNew,
                            0, 0, order_obj.quantity as i32, order_obj.quantity as i32, 0, &ts,
                        );
                        let _ = output_tx.send(ack).await;
                        stop_book.push(order_obj);
                        println!("🛑 Stop {} parked (stop_price={}, last_trade={})", 
                            order.order_id, order.stop_price, last_trade_price);
                        continue;
                    }
                }

                // Process the order (non-stop, or stop that triggered immediately)
                let aggressive = book.process_aggressive(order_obj.clone());

                let mut all_fills        = aggressive.fills;
                let mut all_fill_updates = aggressive.fill_updates;
                let mut incremental_opt  = None;

                if aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
                    let mut resting = order_obj.clone();
                    resting.quantity = aggressive.unfilled_qty;
                    let inc = book.add_order(resting);
                    incremental_opt = Some(inc);
                    let (passive_fills, passive_updates) = book.match_orders();
                    all_fills.extend(passive_fills);
                    all_fill_updates.extend(passive_updates);
                } else if !aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
                    let cancel_report = make_cancel_report(
                        &order.order_id, &symbol, seq, &ts,
                        aggressive.unfilled_qty,
                    );
                    let _ = output_tx.send(cancel_report).await;
                }

                // Update last_trade_price from fills and check stop triggers.
                // Loop handles cascading stops (a triggered stop may itself cause
                // fills that trigger more stops).
                if !all_fills.is_empty() {
                    // Use the last fill price as the new last_trade_price
                    last_trade_price = all_fills.last().unwrap().trade_price;

                    // Check stop triggers — may produce additional fills
                    loop {
                        let triggered = OrderBook::drain_triggered_stops(
                            &mut stop_book, last_trade_price
                        );
                        if triggered.is_empty() { break; }

                        println!("⚡ {} stop(s) triggered at price {}", triggered.len(), last_trade_price);

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
                                resting.quantity   = stop_result.unfilled_qty;
                                let inc = book.add_order(resting);
                                // Broadcast the new resting limit's incremental
                                let md = frame_message(&fxp::FxpMessage {
                                    payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                        fxp::MarketDataIncrementalUpdate {
                                            symbol:    symbol.clone(),
                                            header:    Some(fxp::FxpHeader { protocol_version: "1.1".to_string(), sequence_number: 1 }),
                                            timestamp: ts.clone(),
                                            changes:   inc.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                                update_type: match c.update_type { MarketUpdateType::NewOrder => 0, MarketUpdateType::ModifyOrder => 1, MarketUpdateType::CancelOrder => 2 },
                                                price: c.price, quantity: c.quantity as i32,
                                            }).collect(),
                                        },
                                    )),
                                });
                                broadcast_md(&mut subscribers, md).await;
                                let (extra_fills, extra_updates) = book.match_orders();
                                all_fills.extend(extra_fills.into_iter());
                                all_fill_updates.extend(extra_updates);
                            }

                            // Update last_trade_price if stop produced fills
                            if let Some(p) = stop_last_price {
                                last_trade_price = p;
                            }
                        }
                    }
                }

                // Send ExecutionReports for all fills
                for exec in &all_fills {
                    let make_report = |order_id: String, fully_filled: bool, leaves_qty: i32, cum_qty: i32| -> Vec<u8> {
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                fxp::ExecutionReport {
                                    header: Some(fxp::FxpHeader {
                                        protocol_version: "1.1".to_string(),
                                        sequence_number:  seq,
                                    }),
                                    execution_id:      format!("EXEC-{}-{}", order_id, exec.trade_price),
                                    order_id,
                                    symbol:            symbol.clone(),
                                    execution_type:    fxp::ExecutionType::ExecFilled.into(),
                                    order_status:      if fully_filled {
                                        fxp::OrderStatus::OrderFilled.into()
                                    } else {
                                        fxp::OrderStatus::OrderPartiallyFilled.into()
                                    },
                                    execution_price:   exec.trade_price,
                                    executed_quantity: exec.trade_quantity as i32,
                                    timestamp:         ts.clone(),
                                    // Reconciliation fields
                                    leaves_quantity:   leaves_qty,
                                    avg_px:            exec.trade_price, // single fill: avg = trade price
                                    cum_quantity:      cum_qty,
                                    // Audit fields — populated from order where available
                                    account:           String::new(),
                                    client_order_id:   String::new(),
                                    orig_order_id:     String::new(),
                                    reject_reason:     String::new(),
                                },
                            )),
                        })
                    };
                    let buy_leaves  = if exec.buy_fully_filled  { 0 } else { exec.trade_quantity as i32 };
                    let sell_leaves = if exec.sell_fully_filled { 0 } else { exec.trade_quantity as i32 };
                    let _ = output_tx.send(make_report(exec.buy_order_id.clone(),  exec.buy_fully_filled,  buy_leaves,  exec.trade_quantity as i32)).await;
                    let _ = output_tx.send(make_report(exec.sell_order_id.clone(), exec.sell_fully_filled, sell_leaves, exec.trade_quantity as i32)).await;
                }

                // Broadcast add-order incremental (only for orders that rested on the book)
                if let Some(incremental) = incremental_opt {
                    let md_add_frame = frame_message(&fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                            fxp::MarketDataIncrementalUpdate {
                                symbol:    symbol.clone(),
                                header:    Some(fxp::FxpHeader {
                                    protocol_version: "1.0".to_string(),
                                    sequence_number:  1,
                                }),
                                timestamp: ts.clone(),
                                changes:   incremental.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                    update_type: match c.update_type {
                                        MarketUpdateType::NewOrder    => 0,
                                        MarketUpdateType::ModifyOrder => 1,
                                        MarketUpdateType::CancelOrder => 2,
                                    },
                                    price:    c.price,
                                    quantity: c.quantity as i32,
                                }).collect(),
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
                                symbol:    fill.symbol.clone(),
                                header:    Some(fxp::FxpHeader {
                                    protocol_version: "1.0".to_string(),
                                    sequence_number:  1,
                                }),
                                timestamp: ts.clone(),
                                changes:   fill.changes.iter().map(|c| fxp::MarketOrderUpdate {
                                    update_type: match c.update_type {
                                        MarketUpdateType::NewOrder    => 0,
                                        MarketUpdateType::ModifyOrder => 1,
                                        MarketUpdateType::CancelOrder => 2,
                                    },
                                    price:    c.price,
                                    quantity: c.quantity as i32,
                                }).collect(),
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

#[allow(clippy::too_many_arguments)]
fn make_exec_report(
    order_id:      &str,
    symbol:        &str,
    exec_id_extra: &str,
    exec_type:     fxp::ExecutionType,
    order_status:  fxp::OrderStatus,
    exec_price:    i64,
    exec_qty:      i32,
    _orig_qty:      i32,
    leaves_qty:    i32,
    cum_qty:       i32,
    ts:            &Option<Timestamp>,
) -> Vec<u8> {
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::ExecutionReport(
            fxp::ExecutionReport {
                header:            Some(fxp::FxpHeader { protocol_version: "1.1".to_string(), sequence_number: 0 }),
                execution_id:      format!("{}-{}-{}", exec_type.as_str_name(), order_id, exec_id_extra),
                order_id:          order_id.to_string(),
                symbol:            symbol.to_string(),
                execution_type:    exec_type.into(),
                order_status:      order_status.into(),
                execution_price:   exec_price,
                executed_quantity: exec_qty,
                timestamp:         ts.clone(),
                leaves_quantity:   leaves_qty,
                avg_px:            exec_price,
                cum_quantity:      cum_qty,
                account:           String::new(),
                client_order_id:   String::new(),
                orig_order_id:     String::new(),
                reject_reason:     String::new(),
            },
        )),
    })
}

// ── Cancel report helper ──────────────────────────────────────────────────────
// Sent when an aggressive order (Market/IOC/FOK) has unfilled remainder.

fn make_cancel_report(
    order_id: &str,
    symbol:   &str,
    seq:      u64,
    ts:       &Option<Timestamp>,
    qty:      u32,
) -> Vec<u8> {
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::ExecutionReport(
            fxp::ExecutionReport {
                header: Some(fxp::FxpHeader {
                    protocol_version: "1.1".to_string(),
                    sequence_number:  seq,
                }),
                execution_id:      format!("CANCEL-{}", order_id),
                order_id:          order_id.to_string(),
                symbol:            symbol.to_string(),
                execution_type:    fxp::ExecutionType::ExecCanceled.into(),
                order_status:      fxp::OrderStatus::OrderCanceled.into(),
                execution_price:   0,
                executed_quantity: qty as i32,
                timestamp:         ts.clone(),
                leaves_quantity:   0,
                avg_px:            0,
                cum_quantity:      0,
                account:           String::new(),
                client_order_id:   String::new(),
                orig_order_id:     String::new(),
                reject_reason:     String::new(),
            },
        )),
    })
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

async fn handle_tcp_client(stream: tokio::net::TcpStream, router: SymbolRouter) {
    println!("🧩 New TCP client connected.");

    let (mut read_half, write_half) = stream.into_split();

    // Output channel: all tasks wanting to write to this connection send here.
    // Capacity 8192: large enough to absorb bursts, bounded to signal back-pressure.
    let (output_tx, mut output_rx) = mpsc::channel::<Vec<u8>>(8192);

    // Write task: drains the output channel and writes to the TCP socket.
    // Single consumer = no locking needed on the write half.
    tokio::spawn(async move {
        let mut w = write_half;
        while let Some(frame) = output_rx.recv().await {
            if w.write_all(&frame).await.is_err() {
                break;
            }
        }
        println!("🔌 Write task exiting for connection");
    });

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
                    if tx.send(SymbolMsg::Order(OrderMsg {
                        order,
                        output_tx: output_tx.clone(),
                    })).await.is_err() {
                        println!("❌ Symbol task for {} dropped — channel closed", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::OrderCancel(cancel)) => {
                    let symbol = cancel.symbol.clone();
                    let tx = router.get_or_create(&symbol).await;
                    if tx.send(SymbolMsg::Cancel(CancelMsg {
                        order_id:         cancel.order_id.clone(),
                        client_cancel_id: cancel.client_cancel_id.clone(),
                        output_tx:        output_tx.clone(),
                    })).await.is_err() {
                        println!("❌ Symbol task for {} dropped on cancel", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::OrderCancelReplace(cr)) => {
                    let symbol = cr.symbol.clone();
                    let tx = router.get_or_create(&symbol).await;
                    if tx.send(SymbolMsg::CancelReplace(CancelReplaceMsg {
                        order_id:          cr.order_id.clone(),
                        new_order_id:      cr.new_order_id.clone(),
                        client_replace_id: cr.client_replace_id.clone(),
                        new_price:         cr.new_price,
                        new_quantity:      cr.new_quantity,
                        output_tx:         output_tx.clone(),
                    })).await.is_err() {
                        println!("❌ Symbol task for {} dropped on cancel/replace", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    println!("📡 MarketDataRequest for {:?}", request.symbols);
                    for symbol in &request.symbols {
                        // Per-subscriber market data channel
                        let (md_tx, mut md_rx) = mpsc::channel::<Vec<u8>>(256);
                        let sym_tx = router.get_or_create(symbol).await;
                        let _ = sym_tx.send(SymbolMsg::Subscribe(SubscribeMsg { tx: md_tx })).await;

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
                None        => println!("❌ FXPMessage had no payload"),
            },
            Err(e) => println!("❌ Decode failed: {:?}", e),
        }
    }
}

// ── Entry point ───────────────────────────────────────────────────────────────

pub async fn start_server() {
    let listener = TcpListener::bind("127.0.0.1:8080").await.unwrap();
    let router   = SymbolRouter::new();

    println!("🚀 FXP Core running on 127.0.0.1:8080");
    println!("   Architecture: per-symbol tasks + lock-free output channels");

    loop {
        let (stream, _)  = listener.accept().await.unwrap();
        let router_clone = router.clone();
        tokio::spawn(async move {
            handle_tcp_client(stream, router_clone).await;
        });
    }
}