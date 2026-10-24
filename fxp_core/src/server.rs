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

use crate::fxp;

#[cfg(test)]
use crate::order_book::broadcaster::MarketDataBroadcaster;
#[cfg(test)]
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
#[cfg(test)]
use crate::order_book::order_book::{OrderBook, OrderReplace};

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

struct WallClock;
impl crate::engine::Clock for WallClock {
    fn now(&self) -> crate::engine::Time {
        let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
        crate::engine::Time {
            seconds: now.as_secs() as i64,
            nanos: now.subsec_nanos() as i32,
        }
    }
}
struct GlobalExecIds;
impl crate::engine::ExecIds for GlobalExecIds {
    fn next_id(&mut self) -> String {
        next_exec_id()
    }
}
async fn order_book_task(symbol: String, mut rx: mpsc::Receiver<SymbolMsg>) {
    use crate::engine::{
        CancelMsg as EngineCancel, CancelReplaceMsg as EngineReplace, Engine, Output,
    };
    let mut engine = Engine::new(&symbol, WallClock, GlobalExecIds);
    let mut subscribers: Vec<OutputTx> = Vec::new();
    while let Some(msg) = rx.recv().await {
        let outputs = match msg {
            SymbolMsg::Order(m) => engine.new_order(m.order, m.output_tx),
            SymbolMsg::Cancel(m) => engine.cancel(EngineCancel {
                order_id: m.order_id,
                sender: m.sender,
                client_cancel_id: m.client_cancel_id,
                output_tx: m.output_tx,
            }),
            SymbolMsg::CancelReplace(m) => engine.cancel_replace(EngineReplace {
                order_id: m.order_id,
                sender: m.sender,
                new_order_id: m.new_order_id,
                client_replace_id: m.client_replace_id,
                new_price: m.new_price,
                new_quantity: m.new_quantity,
                new_time_in_force: m.new_time_in_force,
                new_stop_price: m.new_stop_price,
                output_tx: m.output_tx,
            }),
            SymbolMsg::Subscribe(m) => {
                let outputs = engine.subscribe(m.tx.clone());
                if !subscribers.iter().any(|tx| tx.same_channel(&m.tx)) {
                    subscribers.push(m.tx);
                }
                outputs
            }
        };
        for output in outputs {
            match output {
                Output::ExecutionReport(tx, report) => {
                    let _ = tx
                        .send(frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(report)),
                        }))
                        .await;
                }
                Output::Snapshot(tx, snapshot) => {
                    let _ = tx
                        .send(frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataSnapshot(snapshot)),
                        }))
                        .await;
                }
                Output::MarketData(update) => {
                    broadcast_md(
                        &mut subscribers,
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(update)),
                        }),
                    )
                    .await;
                }
            }
        }
    }
}

static NEXT_EXEC_ID: AtomicU64 = AtomicU64::new(1);

/// Engine-wide monotonic execution id, unique across fills at the same price.
fn next_exec_id() -> String {
    format!("X{}", NEXT_EXEC_ID.fetch_add(1, AtomicOrdering::Relaxed))
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

#[cfg(test)]
fn snapshot_frame(book: &OrderBook) -> Vec<u8> {
    use crate::engine::Clock;
    frame_message(&fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataSnapshot(
            crate::engine::snapshot(book, WallClock.now()),
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
    tracing::info!("New TCP client connected.");

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
        tracing::debug!("Write task exiting for connection");
    });
    // When the client leaves, drop its receiver so reports for its resting
    // orders fail fast instead of filling a channel nobody drains.
    let _writer = AbortOnDrop(writer);
    // One market-data channel per symbol for this connection, so subscribing
    // to a symbol again does not stream each update twice.
    let mut md_channels: HashMap<String, mpsc::Sender<Vec<u8>>> = HashMap::new();

    // Read loop: dispatches frames to symbol tasks, never blocks on processing.
    loop {
        let body = match read_framed_message(&mut read_half).await {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => {
                tracing::info!("Client disconnected.");
                return;
            }
            Err(e) => {
                tracing::warn!("Read error: {:?}", e);
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
                        tracing::error!("Symbol task for {} dropped — channel closed", symbol);
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
                        tracing::error!("Symbol task for {} dropped on cancel", symbol);
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
                        tracing::error!("Symbol task for {} dropped on cancel/replace", symbol);
                    }
                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    tracing::debug!("MarketDataRequest for {:?}", request.symbols);
                    for symbol in &request.symbols {
                        let md_tx = match md_channels.get(symbol) {
                            Some(tx) if !tx.is_closed() => tx.clone(),
                            _ => {
                                let (md_tx, mut md_rx) = mpsc::channel::<Vec<u8>>(256);
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
                                md_channels.insert(symbol.clone(), md_tx.clone());
                                md_tx
                            }
                        };
                        let sym_tx = router.get_or_create(symbol).await;
                        let _ = sym_tx
                            .send(SymbolMsg::Subscribe(SubscribeMsg { tx: md_tx }))
                            .await;
                    }
                    tracing::debug!("Subscription setup complete — continuing to accept orders.");
                }

                Some(other) => tracing::warn!("Unhandled payload: {:?}", other),
                None => tracing::warn!("FXPMessage had no payload"),
            },
            Err(e) => tracing::warn!("Decode failed: {:?}", e),
        }
    }
}

const ACCEPT_BACKOFF_MIN: std::time::Duration = std::time::Duration::from_millis(5);
const ACCEPT_BACKOFF_MAX: std::time::Duration = std::time::Duration::from_secs(1);

fn next_accept_backoff(current: std::time::Duration) -> std::time::Duration {
    (current * 2).min(ACCEPT_BACKOFF_MAX)
}

/// Accepts one connection and tunes it for low latency.
async fn accept_client(listener: &TcpListener) -> std::io::Result<tokio::net::TcpStream> {
    let (stream, peer) = listener.accept().await?;
    // Reports are small frames written back-to-back; with Nagle on, the second
    // one waits for the peer's delayed ACK (~40 ms on Linux).
    if let Err(e) = stream.set_nodelay(true) {
        tracing::warn!("set_nodelay failed for {peer}: {e}");
    }
    Ok(stream)
}

// ── Entry point ───────────────────────────────────────────────────────────────

pub async fn start_server() {
    let listener = TcpListener::bind("127.0.0.1:8080").await.unwrap();
    let router = SymbolRouter::new();

    tracing::info!("FXP Core running on 127.0.0.1:8080");
    tracing::info!("Architecture: per-symbol tasks + lock-free output channels");

    let mut backoff = ACCEPT_BACKOFF_MIN;
    loop {
        match accept_client(&listener).await {
            Ok(stream) => {
                backoff = ACCEPT_BACKOFF_MIN;
                let router_clone = router.clone();
                tokio::spawn(async move {
                    handle_tcp_client(stream, router_clone).await;
                });
            }
            // Transient errors (EMFILE, ECONNABORTED, ...) must not kill the server.
            Err(e) => {
                tracing::error!("accept failed: {e}; retrying in {backoff:?}");
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

    #[tokio::test]
    async fn resubscribing_one_channel_streams_market_data_once() {
        let book = spawn_book();
        let (md_tx, mut md_rx) = conn();
        for _ in 0..2 {
            book.send(SymbolMsg::Subscribe(SubscribeMsg { tx: md_tx.clone() }))
                .await
                .unwrap();
        }
        let (a, mut a_rx) = conn();
        send_order(&book, nos("B1", "A", BUY, 10_000, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;
        let mut kinds = Vec::new();
        while let Ok(Some(frame)) =
            tokio::time::timeout(Duration::from_millis(200), md_rx.recv()).await
        {
            kinds.push(
                match fxp::FxpMessage::decode(&frame[4..]).unwrap().payload {
                    Some(fxp::fxp_message::Payload::MarketDataSnapshot(_)) => "W",
                    Some(fxp::fxp_message::Payload::MarketDataIncremental(_)) => "X",
                    _ => "?",
                },
            );
        }
        assert_eq!(
            kinds,
            ["W", "W", "X"],
            "one snapshot per request, one stream"
        );
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
        send_order(&book, nos("S1", "A", SELL, 10_000, 5, LIMIT, GTC), &a).await;
        tokio::time::sleep(Duration::from_millis(20)).await;
        drop(a_rx);
        send_order(&book, nos("B1", "B", BUY, 10_000, 5, LIMIT, DAY), &b).await;
        assert_eq!(et(&next(&mut b_rx).await), fxp::ExecutionType::ExecNew);
        assert_eq!(st(&next(&mut b_rx).await), fxp::OrderStatus::OrderFilled);
    }

    #[tokio::test]
    async fn resting_limit_gets_new_ack() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        send_order(&book, nos("A1", "A", BUY, 10_000, 10, LIMIT, GTC), &a).await;
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

        send_order(&book, nos("S1", "A", SELL, 10_000, 10, LIMIT, GTC), &a).await;
        exec_ids.insert(next(&mut a_rx).await.execution_id);

        send_order(&book, nos("B1", "B", BUY, 10_100, 4, LIMIT, DAY), &b).await;
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
            ("S1", 10_000, 4)
        );
        assert_eq!(
            (s.cum_quantity, s.leaves_quantity, s.avg_px),
            (4, 6, 10_000)
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
            ("B1", 10_000, 4)
        );
        assert_eq!(
            (bf.cum_quantity, bf.leaves_quantity, bf.avg_px),
            (4, 0, 10_000)
        );
        assert_eq!(
            (bf.client_order_id.as_str(), bf.account.as_str()),
            ("CL-B1", "ACC-B")
        );
        exec_ids.insert(bf.execution_id);

        // Second fill at the same price completes the seller.
        send_order(&book, nos("B2", "B", BUY, 10_000, 6, LIMIT, DAY), &b).await;
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
            (6, 10, 0, 10_000)
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
        send_order(&book, nos("S1", "A", SELL, 10_000, 5, LIMIT, GTC), &a).await;
        send_order(&book, nos("S2", "A", SELL, 10_200, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;
        next(&mut a_rx).await;

        send_order(&book, nos("M1", "B", BUY, 0, 10, MARKET, DAY), &b).await;
        let f1 = next(&mut b_rx).await;
        assert_eq!(
            (st(&f1), f1.cum_quantity, f1.leaves_quantity, f1.avg_px),
            (fxp::OrderStatus::OrderPartiallyFilled, 5, 5, 10_000)
        );
        let f2 = next(&mut b_rx).await;
        assert_eq!(
            (st(&f2), f2.cum_quantity, f2.leaves_quantity, f2.avg_px),
            (fxp::OrderStatus::OrderFilled, 10, 0, 10_100)
        );
        assert_ne!(f1.execution_id, f2.execution_id);
        assert_quiet(&mut b_rx).await;
    }

    #[tokio::test]
    async fn ioc_fill_reported_before_remainder_cancel() {
        let book = spawn_book();
        let (a, mut a_rx) = conn();
        let (b, mut b_rx) = conn();
        send_order(&book, nos("S1", "A", SELL, 10_000, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;

        send_order(&book, nos("I1", "B", BUY, 10_000, 8, LIMIT, IOC), &b).await;
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
            (5, 5, 3, 10_000)
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
            (0, 5, 0, 10_000)
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
            nos("Q0", "A", BUY, 10_000, 0, LIMIT, GTC),
            nos("QN", "A", BUY, 10_000, -5, LIMIT, GTC),
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
                nos(id, "A", side, 10_000, 10, order_type, time_in_force),
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
        send_order(&book, nos("R0", "A", BUY, 10_000, 10, LIMIT, GTC), &out).await;
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
        send_order(&book, nos("P0", "A", BUY, 10_000, 10, LIMIT, GTC), &out).await;
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
            10_000,
            10,
            crate::order_book::order::OrderType::Limit,
            crate::order_book::order::TimeInForce::GTC,
        ));
        let (kept, _) = standalone
            .replace_order(
                "P0",
                "P1",
                OrderReplace {
                    new_price: 0,
                    new_total_qty: 0,
                    new_time_in_force: None,
                    client_order_id: "CR2",
                    timestamp: 1,
                },
                "T",
            )
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

    #[tokio::test]
    async fn accepted_sockets_disable_nagle() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let client = tokio::net::TcpStream::connect(addr).await.unwrap();
        let server = accept_client(&listener).await.unwrap();
        assert!(
            server.nodelay().unwrap(),
            "accepted socket must set TCP_NODELAY"
        );
        drop(client);
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
            nos("MB1", "M", BUY, 10_200, 5, LIMIT, GTC),
        )
        .await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MB2", "M", BUY, 10_000, 5, LIMIT, GTC),
        )
        .await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA1", "M", SELL, 10_500, 5, LIMIT, GTC),
        )
        .await;
        rest(&book, &t, &mut t_rx, stop("S", "T", BUY, 10_200, 2)).await;
        // Sell sweep prints 102 then 100: the 102 print must trigger the buy stop.
        send_order(&book, nos("AG", "A", SELL, 0, 10, MARKET, DAY), &conn().0).await;
        assert_eq!(fills(&mut t_rx, 1).await, vec![("S".to_string(), 10_500)]);
    }

    #[tokio::test]
    async fn triggered_stops_fire_by_stop_price_then_arrival() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        for (id, px) in [
            ("MA4", 10_400),
            ("MA5", 10_500),
            ("MA6", 10_600),
            ("MA7", 10_700),
        ] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        for (id, px) in [
            ("S103", 10_300),
            ("S101", 10_100),
            ("S102a", 10_200),
            ("S102b", 10_200),
        ] {
            rest(&book, &t, &mut t_rx, stop(id, "T", BUY, px, 1)).await;
        }
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA3", "M", SELL, 10_300, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 10_300, 1, LIMIT, IOC), &conn().0).await;
        let want = [
            ("S101", 10_400),
            ("S102a", 10_500),
            ("S102b", 10_600),
            ("S103", 10_700),
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
            ("MA104", 10_400),
            ("MA105", 10_500),
            ("MA106", 10_600),
            ("MA107", 10_700),
        ] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        rest(&book, &t, &mut t_rx, stop("S1", "T", BUY, 10_100, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S2", "T", BUY, 10_400, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S3", "T", BUY, 10_500, 1)).await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA101", "M", SELL, 10_100, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 10_100, 1, LIMIT, IOC), &conn().0).await;
        let want: Vec<_> = [("S1", 10_400), ("S2", 10_500), ("S3", 10_600)]
            .iter()
            .map(|(i, p)| (i.to_string(), *p))
            .collect();
        assert_eq!(fills(&mut t_rx, 3).await, want);
        // last_trade_price is now 106: a buy stop at 106 fires on arrival, one at 108 parks.
        send_order(&book, stop("S4", "T", BUY, 10_600, 1), &t).await;
        assert_eq!(fills(&mut t_rx, 1).await, vec![("S4".to_string(), 10_700)]);
        send_order(&book, stop("S5", "T", BUY, 10_800, 1), &t).await;
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
        rest(&book, &t, &mut t_rx, stop("S", "T", SELL, 10_000, 1)).await;
        assert_quiet(&mut t_rx).await;
    }

    #[tokio::test]
    async fn parked_stop_can_be_cancelled_only_by_owner() {
        let book = spawn_book();
        let (m, mut m_rx) = conn();
        let (t, mut t_rx) = conn();
        let (x, mut x_rx) = conn();
        rest(&book, &t, &mut t_rx, stop("S", "T", BUY, 10_000, 1)).await;
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
            nos("MA", "M", SELL, 10_000, 2, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 10_000, 1, LIMIT, IOC), &conn().0).await;
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
            nos("MA101", "M", SELL, 10_100, 2, LIMIT, GTC),
        )
        .await;
        let sl = fxp::NewOrderSingle {
            stop_price: 10_000,
            ..nos("SL", "T", BUY, 10_100, 5, STOP_LIMIT, tif)
        };
        rest(&book, &t, &mut t_rx, sl).await;
        rest(
            &book,
            &m,
            &mut m_rx,
            nos("MA100", "M", SELL, 10_000, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 10_000, 1, LIMIT, IOC), &conn().0).await;
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
        for (id, px) in [("MA110", 11_000), ("MA111", 11_100)] {
            rest(&book, &m, &mut m_rx, nos(id, "M", SELL, px, 1, LIMIT, GTC)).await;
        }
        rest(&book, &t, &mut t_rx, stop("S1", "T", BUY, 10_500, 1)).await;
        rest(&book, &t, &mut t_rx, stop("S2", "T", BUY, 10_300, 1)).await;
        let replace = |sender: &str, out: &OutputTx| {
            SymbolMsg::CancelReplace(CancelReplaceMsg {
                order_id: "S1".into(),
                sender: sender.into(),
                new_order_id: "S1R".into(),
                client_replace_id: format!("CR-{sender}"),
                new_price: 0,
                new_quantity: 0,
                new_time_in_force: 0,
                new_stop_price: 10_100,
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
            nos("MA103", "M", SELL, 10_300, 1, LIMIT, GTC),
        )
        .await;
        send_order(&book, nos("AG", "A", BUY, 10_300, 1, LIMIT, IOC), &conn().0).await;
        let want: Vec<_> = [("S1R", 11_000), ("S2", 11_100)]
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
            book.add_order(Order {
                order_id: id.into(),
                trader_id: "T".into(),
                symbol: "SNAP".into(),
                price: px,
                quantity: qty,
                side,
                order_type: OrderType::Limit,
                time_in_force: TimeInForce::GTC,
                timestamp: 0,
                stop_price: 0,
                expire_date: String::new(),
                account: String::new(),
                client_order_id: String::new(),
            });
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
