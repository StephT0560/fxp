use tokio::net::TcpListener;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::Mutex;
use prost::Message;
use std::sync::Arc;
use std::collections::HashMap;
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::net::tcp::{OwnedReadHalf, OwnedWriteHalf};

mod fxp {
    include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
}

use prost_types::Timestamp;
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::OrderBook;
use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::MarketUpdateType;

#[derive(Debug)]
pub struct MarketDataManager {
    subscribers: Arc<Mutex<HashMap<String, Vec<tokio::sync::mpsc::Sender<Vec<u8>>>>>>,
    order_books: Arc<Mutex<HashMap<String, Arc<Mutex<OrderBook>>>>>,
}

impl MarketDataManager {
    pub fn new() -> Self {
        Self {
            subscribers: Arc::new(Mutex::new(HashMap::new())),
            order_books: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    pub async fn subscribe(&self, symbol: String, tx: tokio::sync::mpsc::Sender<Vec<u8>>) {
        let mut subs = self.subscribers.lock().await;
        subs.entry(symbol.clone()).or_insert_with(Vec::new).push(tx);
        println!("✅ Client subscribed to {}", symbol);
    }

    pub async fn broadcast_update(&self, symbol: &str, update: Vec<u8>) {
        let mut subs = self.subscribers.lock().await;

        if let Some(clients) = subs.get_mut(symbol) {
            let mut disconnected = Vec::new();

            for (i, tx) in clients.iter().enumerate() {
                if tx.send(update.clone()).await.is_err() {
                    disconnected.push(i);
                }
            }

            for &i in disconnected.iter().rev() {
                clients.remove(i);
            }

            if !clients.is_empty() {
                println!(
                    "📡 Market data broadcasted to {} subscribers for {}",
                    clients.len(),
                    symbol
                );
            }
        }
    }

    pub async fn get_or_create_order_book(&self, symbol: &str) -> Arc<Mutex<OrderBook>> {
        let mut books = self.order_books.lock().await;

        if let Some(book) = books.get(symbol) {
            return Arc::clone(book);
        }

        let broadcaster = Arc::new(MarketDataBroadcaster::new());
        let book = Arc::new(Mutex::new(OrderBook::new(symbol, broadcaster)));
        books.insert(symbol.to_string(), Arc::clone(&book));
        book
    }
}

// ──────────────────────────────────────────────────────────────
// Read a single length-prefixed FXP message from the read half.
// Wire format (matching Go gateway): [u32 big-endian len][body]
// ──────────────────────────────────────────────────────────────
async fn read_framed_message(read_half: &mut OwnedReadHalf) -> Result<Vec<u8>, std::io::Error> {
    // Read the 4-byte length prefix
    let mut len_buf = [0u8; 4];
    read_half.read_exact(&mut len_buf).await?;
    let msg_len = u32::from_be_bytes(len_buf) as usize;

    // Sanity cap: 1 MB. Raise if needed for deep order book snapshots.
    if msg_len == 0 || msg_len > 1_048_576 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("Invalid FXP message length: {}", msg_len),
        ));
    }

    // Read the message body
    let mut body = vec![0u8; msg_len];
    read_half.read_exact(&mut body).await?;
    Ok(body)
}

// ──────────────────────────────────────────────────────────────
// Encode a Protobuf message with a 4-byte big-endian length prefix.
// ──────────────────────────────────────────────────────────────
fn frame_message(msg: &fxp::FxpMessage) -> Vec<u8> {
    let mut encoded = Vec::new();
    msg.encode(&mut encoded).unwrap();
    let mut framed = (encoded.len() as u32).to_be_bytes().to_vec();
    framed.extend_from_slice(&encoded);
    framed
}

async fn handle_tcp_client(
    stream: tokio::net::TcpStream,
    market_data: Arc<MarketDataManager>,
) {
    println!("🧩 New TCP client connected.");

    let (mut read_half, write_half) = stream.into_split();
    let write_half = Arc::new(Mutex::new(write_half));

    loop {
        println!("🧪 Waiting for incoming message...");

        // FIX: read a properly length-prefixed frame, not a raw buffer read
        let body = match read_framed_message(&mut read_half).await {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => {
                println!("🔌 Client disconnected.");
                return;
            }
            Err(e) => {
                println!("❌ Failed to read framed message: {:?}", e);
                return;
            }
        };

        println!("📩 Received {} bytes from client", body.len());

        match fxp::FxpMessage::decode(body.as_slice()) {
            Ok(msg) => match msg.payload {
                Some(fxp::fxp_message::Payload::NewOrder(order)) => {
                    println!("📩 Parsed NewOrder: {:?}", order);

                    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
                    let unix_ms = now.as_millis() as u64;

                    let order_obj = Order {
                        order_id: order.order_id.clone(),
                        trader_id: order.sender.clone(),
                        symbol: order.symbol.clone(),
                        price: order.price,
                        quantity: order.quantity as u32,
                        side: match order.side {
                            0 => OrderSide::Buy,
                            _ => OrderSide::Sell,
                        },
                        order_type: OrderType::Limit,
                        time_in_force: TimeInForce::Day,
                        timestamp: unix_ms,
                    };

                    let book = market_data.get_or_create_order_book(&order.symbol).await;
                    let mut book = book.lock().await;

                    let incremental = book.add_order(order_obj.clone());
                    let (matches, fill_updates) = book.match_orders();

                    // Send ExecutionReports for both sides of each fill.
                    // buy_fully_filled / sell_fully_filled let the gateway
                    // know whether to keep or remove the client mapping.
                    for exec in &matches {
                        let seq = order.header.as_ref().map_or(0, |h| h.sequence_number + 1);

                        // Helper closure to build and send one report
                        let make_report = |order_id: String, fully_filled: bool| {
                            fxp::FxpMessage {
                                payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                    fxp::ExecutionReport {
                                        header: Some(fxp::FxpHeader {
                                            protocol_version: "1.0".to_string(),
                                            sequence_number: seq,
                                        }),
                                        execution_id: format!("EXEC-{}-{}", order_id, exec.trade_price),
                                        order_id,
                                        symbol: order.symbol.clone(),
                                        execution_type: fxp::ExecutionType::ExecFilled.into(),
                                        order_status: if fully_filled {
                                            fxp::OrderStatus::OrderFilled.into()
                                        } else {
                                            fxp::OrderStatus::OrderPartiallyFilled.into()
                                        },
                                        execution_price: exec.trade_price,
                                        executed_quantity: exec.trade_quantity as i32,
                                        timestamp: Some(Timestamp {
                                            seconds: now.as_secs() as i64,
                                            nanos: now.subsec_nanos() as i32,
                                        }),
                                    },
                                )),
                            }
                        };

                        // Report for the buy side
                        let buy_report = make_report(
                            exec.buy_order_id.clone(),
                            exec.buy_fully_filled,
                        );
                        // Report for the sell side
                        let sell_report = make_report(
                            exec.sell_order_id.clone(),
                            exec.sell_fully_filled,
                        );

                        let mut writer = write_half.lock().await;
                        writer.write_all(&frame_message(&buy_report)).await.unwrap();
                        writer.write_all(&frame_message(&sell_report)).await.unwrap();
                    }

                    // Broadcast the add_order incremental update (new order on book)
                    let md_add = fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                            fxp::MarketDataIncrementalUpdate {
                                symbol: incremental.symbol.clone(),
                                header: Some(fxp::FxpHeader {
                                    protocol_version: "1.0".to_string(),
                                    sequence_number: 1,
                                }),
                                timestamp: Some(Timestamp {
                                    seconds: now.as_secs() as i64,
                                    nanos: now.subsec_nanos() as i32,
                                }),
                                changes: incremental
                                    .changes
                                    .into_iter()
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
                    };
                    market_data
                        .broadcast_update(&order.symbol, frame_message(&md_add))
                        .await;

                    // Broadcast fill incremental updates (modify/remove levels)
                    for fill_update in fill_updates {
                        let md_fill = fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                                fxp::MarketDataIncrementalUpdate {
                                    symbol: fill_update.symbol.clone(),
                                    header: Some(fxp::FxpHeader {
                                        protocol_version: "1.0".to_string(),
                                        sequence_number: 1,
                                    }),
                                    timestamp: Some(Timestamp {
                                        seconds: now.as_secs() as i64,
                                        nanos: now.subsec_nanos() as i32,
                                    }),
                                    changes: fill_update
                                        .changes
                                        .into_iter()
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
                        };
                        market_data
                            .broadcast_update(&fill_update.symbol, frame_message(&md_fill))
                            .await;
                    }
                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    println!("📡 Market Data Subscription Request: {:?}", request);

                    for symbol in request.symbols {
                        let (client_tx, mut client_rx) =
                            tokio::sync::mpsc::channel::<Vec<u8>>(10);
                        market_data.subscribe(symbol.clone(), client_tx).await;

                        let write_half = Arc::clone(&write_half);
                        tokio::spawn(async move {
                            println!("🧵 Market data task started for: {}", symbol);
                            while let Some(update) = client_rx.recv().await {
                                let mut writer = write_half.lock().await;
                                if writer.write_all(&update).await.is_err() {
                                    println!("❌ Failed to send market update.");
                                    break;
                                }
                                println!("✅ Market update sent to client.");
                            }
                        });
                    }

                    println!("🧼 Subscription setup complete — continuing to accept orders.");
                }

                Some(other) => {
                    println!("⚠️ Unhandled FXPMessage payload: {:?}", other);
                }

                None => {
                    println!("❌ FXPMessage had no payload");
                }
            },

            Err(e) => {
                println!("❌ FXPMessage decoding failed: {:?}", e);
                let mut writer = write_half.lock().await;
                let _ = writer
                    .write_all(b"Invalid FXPMessage format")
                    .await;
            }
        }
    }
}

pub async fn start_server() {
    let tcp_listener = TcpListener::bind("127.0.0.1:8080").await.unwrap();
    let market_data = Arc::new(MarketDataManager::new());

    println!("🚀 FXP Core running on 127.0.0.1:8080");

    loop {
        let (stream, _) = tcp_listener.accept().await.unwrap();
        let market_data_clone = market_data.clone();
        tokio::spawn(async move {
            handle_tcp_client(stream, market_data_clone).await;
        });
    }
}