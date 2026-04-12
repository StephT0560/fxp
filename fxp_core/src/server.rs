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
use crate::order_book::matcher::AggressiveResult;

// ── Output channel ────────────────────────────────────────────────────────────
// Each connection gets one output channel. All tasks that need to write to
// this connection send frames here. The write_task drains it exclusively.
type OutputTx = mpsc::Sender<Vec<u8>>;

// ── Messages dispatched to a symbol's task ────────────────────────────────────

struct OrderMsg {
    order:     fxp::NewOrderSingle,
    output_tx: OutputTx,  // channel back to the originating connection's writer
}

struct SubscribeMsg {
    tx: mpsc::Sender<Vec<u8>>,  // per-subscriber market data channel
}

enum SymbolMsg {
    Order(OrderMsg),
    Subscribe(SubscribeMsg),
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

    println!("📚 Order book task started for {}", symbol);

    while let Some(msg) = rx.recv().await {
        match msg {
            SymbolMsg::Subscribe(sub) => {
                subscribers.push(sub.tx);
                println!("✅ Client subscribed to {}", symbol);
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
                        _ => OrderType::Market, // 0 = Market, default
                    },
                    time_in_force: match order.time_in_force {
                        1 => TimeInForce::GTC,
                        2 => TimeInForce::IOC,
                        3 => TimeInForce::FOK,
                        _ => TimeInForce::Day, // 0 = Day, default
                    },
                    timestamp: now.as_millis() as u64,
                };

                let seq = order.header.as_ref().map_or(0, |h| h.sequence_number + 1);
                let ts  = Some(Timestamp {
                    seconds: now.as_secs() as i64,
                    nanos:   now.subsec_nanos() as i32,
                });

                // Step 1: check if order is aggressive (Market/IOC/FOK)
                // or resting (Limit Day/GTC). Aggressive orders are matched
                // immediately without being added to the book.
                let aggressive = book.process_aggressive(order_obj.clone());

                // Collect all fills and market data updates
                let mut all_fills        = aggressive.fills;
                let mut all_fill_updates = aggressive.fill_updates;
                let mut incremental_opt  = None;

                if aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
                    // Resting order: add to book and run passive matching
                    let mut resting = order_obj.clone();
                    resting.quantity = aggressive.unfilled_qty;
                    let inc = book.add_order(resting);
                    incremental_opt = Some(inc);
                    let (passive_fills, passive_updates) = book.match_orders();
                    all_fills.extend(passive_fills);
                    all_fill_updates.extend(passive_updates);
                } else if !aggressive.rest_on_book && aggressive.unfilled_qty > 0 {
                    // Aggressive order with unfilled remainder — send cancel report
                    let cancel_report = make_cancel_report(
                        &order.order_id, &symbol, seq, &ts,
                        aggressive.unfilled_qty,
                    );
                    let _ = output_tx.send(cancel_report).await;
                }

                // Send ExecutionReports for all fills
                for exec in &all_fills {
                    let make_report = |order_id: String, fully_filled: bool| -> Vec<u8> {
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                fxp::ExecutionReport {
                                    header: Some(fxp::FxpHeader {
                                        protocol_version: "1.0".to_string(),
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
                                },
                            )),
                        })
                    };
                    let _ = output_tx.send(make_report(exec.buy_order_id.clone(),  exec.buy_fully_filled)).await;
                    let _ = output_tx.send(make_report(exec.sell_order_id.clone(), exec.sell_fully_filled)).await;
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
                    protocol_version: "1.0".to_string(),
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