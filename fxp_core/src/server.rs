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
use crate::order_book::market_data::MarketOrderUpdate;

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
            let mut disconnected_clients = Vec::new();

            for (i, tx) in clients.iter().enumerate() {
                if tx.send(update.clone()).await.is_err() {
                    disconnected_clients.push(i);
                }
            }

            for &i in disconnected_clients.iter().rev() {
                clients.remove(i);
            }

            if !clients.is_empty() {
                println!("📡 Market data update broadcasted to {} subscribers for {}", clients.len(), symbol);
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

async fn handle_tcp_client(
    stream: tokio::net::TcpStream,
    market_data: Arc<MarketDataManager>,
) {
    println!("🧩 New TCP client connected.");

    let (mut read_half, write_half): (OwnedReadHalf, OwnedWriteHalf) = stream.into_split();
    let write_half = Arc::new(Mutex::new(write_half));
    let mut buffer = vec![0; 1024];

    loop {
        println!("🧪 Waiting for incoming message...");
        let n = match read_half.read(&mut buffer).await {
            Ok(n) if n > 0 => {
                println!("📩 Received {} bytes from client", n);
                n
            }
            Ok(_) => return,
            Err(e) => {
                println!("❌ Failed to read from TCP client: {:?}", e);
                return;
            }
        };

        match fxp::FxpMessage::decode(&mut &buffer[..n]) {
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
                            1 => OrderSide::Sell,
                            _ => OrderSide::Buy,
                        },
                        order_type: OrderType::Limit,
                        time_in_force: TimeInForce::Day,
                        timestamp: unix_ms,
                    };

                    let book = market_data.get_or_create_order_book(&order.symbol).await;
                    let mut book = book.lock().await;

                    let incremental = book.add_order(order_obj.clone());
                    let matches = book.match_orders();

                    for exec in matches {
                        let fxp_report = fxp::ExecutionReport {
                            header: Some(fxp::FxpHeader {
                                protocol_version: "1.0".to_string(),
                                sequence_number: order.header.as_ref().map_or(0, |h| h.sequence_number + 1),
                            }),
                            execution_id: format!("EXEC{}", exec.buy_order_id),
                            order_id: exec.buy_order_id,
                            symbol: order.symbol.clone(),
                            execution_type: fxp::ExecutionType::ExecFilled.into(),
                            order_status: fxp::OrderStatus::OrderFilled.into(),
                            execution_price: exec.trade_price,
                            executed_quantity: exec.trade_quantity as i32,
                            timestamp: Some(Timestamp {
                                seconds: now.as_secs() as i64,
                                nanos: now.subsec_nanos() as i32,
                            }),
                        };

                        let response = fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(fxp_report)),
                        };

                        let mut encoded = Vec::new();
                        response.encode(&mut encoded).unwrap();
                        
                        // Add 4-byte length prefix
                        let mut framed = (encoded.len() as u32).to_be_bytes().to_vec();
                        framed.extend_from_slice(&encoded);
                        
                        let mut writer = write_half.lock().await;
                        writer.write_all(&framed).await.unwrap();
                        
                    }

                    let md_update = fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
                            fxp::MarketDataIncrementalUpdate {
                                symbol: incremental.symbol.clone(),
                                header: Some(fxp::FxpHeader {
                                    protocol_version: "1.0".to_string(),
                                    sequence_number: 1, // Optional: track properly later
                                }),
                                timestamp: Some(Timestamp {
                                    seconds: now.as_secs() as i64,
                                    nanos: now.subsec_nanos() as i32,
                                }),                                
                                changes: incremental.changes.into_iter().map(|c| fxp::MarketOrderUpdate {
                                    update_type: match c.update_type {
                                        MarketUpdateType::NewOrder => 0,
                                        MarketUpdateType::ModifyOrder => 1,
                                        MarketUpdateType::CancelOrder => 2,
                                    },
                                    price: c.price,
                                    quantity: c.quantity as i32,
                                }).collect(),
                            }
                        )),
                    };

                    let mut encoded = Vec::new();
                    md_update.encode(&mut encoded).unwrap();
                    
                    let mut framed = (encoded.len() as u32).to_be_bytes().to_vec();
                    framed.extend_from_slice(&encoded);
                    
                    let mut framed = (encoded.len() as u32).to_be_bytes().to_vec();
                    framed.extend_from_slice(&encoded);
                    market_data.broadcast_update(&order.symbol, framed).await;

                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    println!("📡 Market Data Subscription Request Received: {:?}", request);

                    for symbol in request.symbols {
                        let (client_tx, mut client_rx) = tokio::sync::mpsc::channel::<Vec<u8>>(10);
                        market_data.subscribe(symbol.clone(), client_tx).await;

                        let write_half = Arc::clone(&write_half);
                        tokio::spawn(async move {
                            println!("🧵 Market data task started for symbol: {}", symbol);
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

                    println!("🧼 Returning early after subscription setup.");
                    return;
                }

                Some(other) => {
                    println!("⚠️ Received unhandled FXPMessage payload: {:?}", other);
                }

                None => {
                    println!("❌ FXPMessage had no payload");
                }
            }

            Err(e) => {
                println!("❌ FXPMessage decoding failed: {:?}", e);
                let mut writer = write_half.lock().await;
                let _ = writer.write_all(b"Invalid FXPMessage format").await;
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
