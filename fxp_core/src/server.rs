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
use std::collections::{HashMap, HashSet};
use std::future::Future;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering as AtomicOrdering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio::net::tcp::OwnedReadHalf;
use tokio::sync::Mutex;
use tokio::sync::mpsc;

use crate::engine::SessionSchedule;
use crate::event_log::{Input, Journal};
use crate::fxp;
use crate::order_book::order::StpMode;

type Persistence = Arc<std::sync::Mutex<Journal<OutputTx>>>;

fn persistence_failure(error: impl std::fmt::Display) -> ! {
    tracing::error!("core event-log failure: {error}");
    std::process::exit(1);
}

fn persisted_input(
    runtime: &Persistence,
    symbol: &str,
    msg: Option<SymbolMsg>,
) -> Vec<crate::engine::Output<OutputTx>> {
    use crate::engine::Clock;
    let mut journal = runtime
        .lock()
        .unwrap_or_else(|error| persistence_failure(error));
    let at = WallClock.now();
    let result = match msg {
        None => journal.tick(symbol, at),
        Some(SymbolMsg::Order(m)) => journal.apply(symbol, at, Input::New(m.order), m.output_tx),
        Some(SymbolMsg::Subscribe(m)) => journal.apply(symbol, at, Input::Subscribe, m.tx),
        Some(SymbolMsg::Cancel(m)) => journal.apply(
            symbol,
            at,
            Input::Cancel(fxp::OrderCancelRequest {
                symbol: symbol.into(),
                order_id: m.order_id,
                sender: m.sender,
                client_cancel_id: m.client_cancel_id,
                ..Default::default()
            }),
            m.output_tx,
        ),
        Some(SymbolMsg::CancelReplace(m)) => journal.apply(
            symbol,
            at,
            Input::Replace(fxp::OrderCancelReplaceRequest {
                symbol: symbol.into(),
                order_id: m.order_id,
                sender: m.sender,
                new_order_id: m.new_order_id,
                client_replace_id: m.client_replace_id,
                new_price: m.new_price,
                new_quantity: m.new_quantity,
                new_time_in_force: m.new_time_in_force,
                new_stop_price: m.new_stop_price,
                ..Default::default()
            }),
            m.output_tx,
        ),
        Some(SymbolMsg::MassCancel(m)) => journal.apply(
            symbol,
            at,
            Input::MassCancel(fxp::OrderMassCancelRequest {
                symbol: symbol.into(),
                header: None,
                timestamp: None,
                ..m.request
            }),
            m.output_tx,
        ),
    };
    result.unwrap_or_else(|error| persistence_failure(error))
}

#[cfg(test)]
use crate::order_book::broadcaster::MarketDataBroadcaster;
#[cfg(test)]
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
#[cfg(test)]
use crate::order_book::order_book::{OrderBook, OrderReplace};

// ── Output channel ────────────────────────────────────────────────────────────
// Each connection has separate bounded queues for critical reports and
// droppable market data. A single writer drains both queues.
#[derive(Clone)]
struct ClientTx {
    frames: mpsc::Sender<Vec<u8>>,
    md: mpsc::Sender<Vec<u8>>,
    slow: Arc<tokio::sync::Notify>,
    slow_reported: Arc<std::sync::atomic::AtomicBool>,
    md_dropped: Arc<AtomicU64>,
}

static MD_DROPPED: AtomicU64 = AtomicU64::new(0);

impl ClientTx {
    fn channel(
        frame_capacity: usize,
        md_capacity: usize,
    ) -> (Self, mpsc::Receiver<Vec<u8>>, mpsc::Receiver<Vec<u8>>) {
        let (frames, frame_rx) = mpsc::channel(frame_capacity);
        let (md, md_rx) = mpsc::channel(md_capacity);
        (
            Self {
                frames,
                md,
                slow: Arc::new(tokio::sync::Notify::new()),
                slow_reported: Arc::new(std::sync::atomic::AtomicBool::new(false)),
                md_dropped: Arc::new(AtomicU64::new(0)),
            },
            frame_rx,
            md_rx,
        )
    }

    fn same_channel(&self, other: &Self) -> bool {
        self.frames.same_channel(&other.frames)
    }

    fn try_send_frame(&self, frame: Vec<u8>) {
        if let Err(mpsc::error::TrySendError::Full(_)) = self.frames.try_send(frame)
            && !self
                .slow_reported
                .swap(true, std::sync::atomic::Ordering::Relaxed)
        {
            tracing::warn!("critical output queue full; disconnecting slow consumer");
            self.slow.notify_one();
        }
    }

    // Returns false only after the subscriber is closed. A full queue drops
    // this market-data frame but leaves the subscriber registered.
    fn try_send_md(&self, frame: Vec<u8>) -> bool {
        match self.md.try_send(frame) {
            Ok(()) => true,
            Err(mpsc::error::TrySendError::Full(_)) => {
                MD_DROPPED.fetch_add(1, AtomicOrdering::Relaxed);
                self.md_dropped.fetch_add(1, AtomicOrdering::Relaxed);
                true
            }
            Err(mpsc::error::TrySendError::Closed(_)) => false,
        }
    }
}

type OutputTx = ClientTx;

// ── Messages dispatched to a symbol's task ────────────────────────────────────

struct OrderMsg {
    order: fxp::NewOrderSingle,
    output_tx: OutputTx,
}

struct SubscribeMsg {
    tx: OutputTx,
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

struct MassCancelMsg {
    request: fxp::OrderMassCancelRequest,
    output_tx: OutputTx,
    reply: Option<tokio::sync::oneshot::Sender<u32>>,
}

enum SymbolMsg {
    Order(OrderMsg),
    Subscribe(SubscribeMsg),
    Cancel(CancelMsg),
    CancelReplace(CancelReplaceMsg),
    MassCancel(MassCancelMsg),
}

// ── Symbol router ─────────────────────────────────────────────────────────────

#[derive(Clone)]
struct SymbolRouter {
    channels: Arc<Mutex<HashMap<String, mpsc::Sender<SymbolMsg>>>>,
    tasks: Arc<std::sync::Mutex<Vec<tokio::task::JoinHandle<()>>>>,
    #[cfg(test)]
    test_symbol_gate: Option<Arc<tokio::sync::Semaphore>>,
    schedule: Option<SessionSchedule>,
    stp_default: StpMode,
    persistence: Option<Persistence>,
    allowed_symbols: Option<Arc<HashSet<String>>>,
    max_symbols: Option<usize>,
}

impl SymbolRouter {
    fn new(schedule: Option<SessionSchedule>) -> Self {
        Self {
            channels: Arc::new(Mutex::new(HashMap::new())),
            tasks: Arc::new(std::sync::Mutex::new(Vec::new())),
            #[cfg(test)]
            test_symbol_gate: None,
            schedule,
            stp_default: StpMode::None,
            persistence: None,
            allowed_symbols: None,
            max_symbols: None,
        }
    }

    async fn symbols(&self) -> Vec<String> {
        let mut symbols: Vec<_> = self.channels.lock().await.keys().cloned().collect();
        symbols.sort();
        symbols
    }

    async fn get_existing(&self, symbol: &str) -> Option<mpsc::Sender<SymbolMsg>> {
        self.channels.lock().await.get(symbol).cloned()
    }

    async fn stop_symbols(&self) -> Vec<tokio::task::JoinHandle<()>> {
        self.channels.lock().await.clear();
        std::mem::take(&mut *self.tasks.lock().expect("symbol task list lock poisoned"))
    }

    async fn get_or_admit(&self, symbol: &str) -> Result<mpsc::Sender<SymbolMsg>, &'static str> {
        if symbol.is_empty() {
            return Err("symbol must not be empty");
        }
        // Fast path: symbol already exists.
        {
            let map = self.channels.lock().await;
            if let Some(tx) = map.get(symbol) {
                return Ok(tx.clone());
            }
        }
        // Slow path: first time seeing this symbol.
        let mut map = self.channels.lock().await;
        // Re-check after acquiring write lock — another task may have raced us.
        if let Some(tx) = map.get(symbol) {
            return Ok(tx.clone());
        }
        if self
            .allowed_symbols
            .as_ref()
            .is_some_and(|allowed| !allowed.contains(symbol))
        {
            return Err("symbol is not allowed");
        }
        if self.max_symbols.is_some_and(|limit| map.len() >= limit) {
            return Err("maximum symbol count reached");
        }
        Ok(self.create_symbol_locked(&mut map, symbol))
    }

    async fn restore_symbol(&self, symbol: &str) {
        let mut map = self.channels.lock().await;
        if map.contains_key(symbol) {
            return;
        }
        let disallowed = self
            .allowed_symbols
            .as_ref()
            .is_some_and(|allowed| !allowed.contains(symbol));
        let over_limit = self.max_symbols.is_some_and(|limit| map.len() >= limit);
        if disallowed || over_limit || symbol.is_empty() {
            tracing::warn!(
                symbol,
                disallowed,
                over_limit,
                "restoring persisted symbol outside admission settings"
            );
        }
        self.create_symbol_locked(&mut map, symbol);
    }

    fn create_symbol_locked(
        &self,
        map: &mut HashMap<String, mpsc::Sender<SymbolMsg>>,
        symbol: &str,
    ) -> mpsc::Sender<SymbolMsg> {
        let (tx, rx) = mpsc::channel::<SymbolMsg>(4096);
        map.insert(symbol.to_string(), tx.clone());
        let sym = symbol.to_string();
        let schedule = self.schedule;
        let stp_default = self.stp_default;
        let persistence = self.persistence.clone();
        // Spawn while holding the lock to guarantee the entry is visible
        // before any message can be dispatched to this symbol.
        #[cfg(test)]
        let task = {
            if let Some(gate) = self.test_symbol_gate.clone() {
                tokio::spawn(async move {
                    gate.acquire()
                        .await
                        .expect("test symbol gate closed")
                        .forget();
                    scheduled_order_book_task(sym, rx, schedule, stp_default, persistence).await;
                })
            } else {
                tokio::spawn(scheduled_order_book_task(
                    sym,
                    rx,
                    schedule,
                    stp_default,
                    persistence,
                ))
            }
        };
        #[cfg(not(test))]
        let task = tokio::spawn(scheduled_order_book_task(
            sym,
            rx,
            schedule,
            stp_default,
            persistence,
        ));
        self.tasks
            .lock()
            .expect("symbol task list lock poisoned")
            .push(task);
        tx
    }
}

#[doc(hidden)]
pub mod bench_support {
    use super::*;

    pub struct RouterBenchmark {
        router: SymbolRouter,
        symbols: Arc<Vec<String>>,
    }

    impl RouterBenchmark {
        pub async fn new(symbol_count: usize) -> Self {
            let symbols: Arc<Vec<_>> = Arc::new(
                (0..symbol_count.max(1))
                    .map(|index| format!("BENCH{index}"))
                    .collect(),
            );
            let router = SymbolRouter::new(None);
            for symbol in symbols.iter() {
                router
                    .get_or_admit(symbol)
                    .await
                    .expect("bench symbols are pre-admitted");
            }
            Self { router, symbols }
        }

        pub async fn concurrent_lookups(&self, task_count: usize, lookups_per_task: usize) {
            let mut tasks = tokio::task::JoinSet::new();
            for task_index in 0..task_count.max(1) {
                let router = self.router.clone();
                let symbols = self.symbols.clone();
                tasks.spawn(async move {
                    for lookup_index in 0..lookups_per_task {
                        let symbol = &symbols[(lookup_index + task_index) % symbols.len()];
                        router
                            .get_or_admit(symbol)
                            .await
                            .expect("bench symbols are pre-admitted");
                    }
                });
            }
            while tasks.join_next().await.is_some() {}
        }
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
#[cfg(test)]
async fn order_book_task(symbol: String, rx: mpsc::Receiver<SymbolMsg>) {
    scheduled_order_book_task(symbol, rx, None, StpMode::None, None).await;
}

async fn scheduled_order_book_task(
    symbol: String,
    mut rx: mpsc::Receiver<SymbolMsg>,
    schedule: Option<SessionSchedule>,
    stp_default: StpMode,
    persistence: Option<Persistence>,
) {
    use crate::engine::{
        CancelMsg as EngineCancel, CancelReplaceMsg as EngineReplace, Engine,
        MassCancelMsg as EngineMassCancel, Output,
    };
    let mut engine = persistence.is_none().then(|| {
        Engine::new(&symbol, WallClock, GlobalExecIds, schedule).with_stp_default(stp_default)
    });
    let mut subscribers: Vec<OutputTx> = Vec::new();
    let mut interval = tokio::time::interval(std::time::Duration::from_secs(1));
    loop {
        let msg = tokio::select! {
            _ = interval.tick() => None,
            msg = rx.recv() => {
                let Some(msg) = msg else { break; };
                Some(msg)
            }
        };
        let mut reply = None;
        let msg = msg.map(|msg| match msg {
            SymbolMsg::MassCancel(mut m) => {
                reply = m.reply.take();
                SymbolMsg::MassCancel(m)
            }
            other => other,
        });
        let outputs = if let Some(runtime) = &persistence {
            persisted_input(runtime, &symbol, msg)
        } else {
            let engine = engine.as_mut().expect("unlogged engine");
            match msg {
                None => engine.tick(),
                Some(msg) => match msg {
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
                    SymbolMsg::Subscribe(m) => engine.subscribe(m.tx),
                    SymbolMsg::MassCancel(m) => engine.mass_cancel(EngineMassCancel {
                        sender: m.request.sender,
                        side: m.request.side,
                        client_request_id: m.request.client_request_id,
                        reason: m.request.reason,
                    }),
                },
            }
        };
        for output in outputs {
            match output {
                Output::MassCancelCount(n) => {
                    if let Some(reply) = reply.take() {
                        let _ = reply.send(n);
                    }
                }
                Output::TradingSessionStatus(status) => {
                    broadcast_md(
                        &mut subscribers,
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::TradingSessionStatus(status)),
                        }),
                    );
                }
                Output::AuctionInfo(info) => {
                    broadcast_md(
                        &mut subscribers,
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::AuctionInfo(info)),
                        }),
                    );
                }
                Output::ExecutionReport(tx, report) => {
                    tx.try_send_frame(frame_message(&fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::ExecutionReport(report)),
                    }));
                }
                Output::Snapshot(tx, snapshot) => {
                    if tx.try_send_md(frame_message(&fxp::FxpMessage {
                        payload: Some(fxp::fxp_message::Payload::MarketDataSnapshot(snapshot)),
                    })) && !subscribers.iter().any(|sub| sub.same_channel(&tx))
                    {
                        subscribers.push(tx);
                    }
                }
                Output::MarketData(update) => {
                    broadcast_md(
                        &mut subscribers,
                        frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(update)),
                        }),
                    );
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

fn broadcast_md(subscribers: &mut Vec<ClientTx>, frame: Vec<u8>) {
    let mut dead = vec![];
    for (i, tx) in subscribers.iter().enumerate() {
        if !tx.try_send_md(frame.clone()) {
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

struct AbortOnDrop(Option<tokio::task::JoinHandle<()>>);

impl Drop for AbortOnDrop {
    fn drop(&mut self) {
        if let Some(handle) = self.0.take() {
            handle.abort();
        }
    }
}

impl AbortOnDrop {
    fn detach(mut self) -> tokio::task::JoinHandle<()> {
        self.0.take().expect("writer handle present")
    }
}

#[cfg(test)]
async fn handle_tcp_client(stream: tokio::net::TcpStream, router: SymbolRouter) {
    let (_shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
    let (writer_tx, mut writer_rx) = mpsc::unbounded_channel();
    handle_tcp_client_with_shutdown(stream, router, shutdown_rx.clone(), shutdown_rx, writer_tx)
        .await;
    while let Ok(writer) = writer_rx.try_recv() {
        writer.abort();
    }
}

async fn handle_tcp_client_with_shutdown(
    stream: tokio::net::TcpStream,
    router: SymbolRouter,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
    mut writer_drain: tokio::sync::watch::Receiver<bool>,
    writer_tasks: mpsc::UnboundedSender<tokio::task::JoinHandle<()>>,
) {
    tracing::info!("New TCP client connected.");

    let (mut read_half, write_half) = stream.into_split();

    let (output_tx, mut output_rx, mut md_rx) = ClientTx::channel(8192, 4096);

    // One writer serializes frames and prioritizes critical reports over MD.
    let writer = tokio::spawn(async move {
        let mut w = write_half;
        let (mut frames_open, mut md_open) = (true, true);
        while frames_open || md_open {
            let frame = tokio::select! {
                biased;
                changed = writer_drain.changed() => {
                    if changed.is_err() || *writer_drain.borrow() {
                        while let Ok(frame) = output_rx.try_recv() {
                            if w.write_all(&frame).await.is_err() {
                                break;
                            }
                        }
                        while let Ok(frame) = md_rx.try_recv() {
                            if w.write_all(&frame).await.is_err() {
                                break;
                            }
                        }
                        let _ = w.flush().await;
                        let _ = w.shutdown().await;
                        break;
                    }
                    continue;
                }
                frame = output_rx.recv(), if frames_open => match frame {
                    Some(frame) => Some(frame),
                    None => {
                        frames_open = false;
                        None
                    }
                },
                frame = md_rx.recv(), if md_open => match frame {
                    Some(frame) => Some(frame),
                    None => {
                        md_open = false;
                        None
                    }
                },
            };
            if let Some(frame) = frame
                && w.write_all(&frame).await.is_err()
            {
                break;
            }
        }
        tracing::debug!("Write task exiting for connection");
    });
    // When the client leaves, drop its receiver so reports for its resting
    // orders fail fast instead of filling a channel nobody drains.
    let mut writer_guard = Some(AbortOnDrop(Some(writer)));

    // Read loop: dispatches frames to symbol tasks, never blocks on processing.
    loop {
        let body = tokio::select! {
            biased;
            _ = output_tx.slow.notified() => {
                tracing::warn!("disconnecting slow consumer after critical output overflow");
                return;
            }
            changed = shutdown.changed() => {
                if changed.is_err() || *shutdown.borrow() {
                    if let Some(writer) = writer_guard.take() {
                        let _ = writer_tasks.send(writer.detach());
                    }
                    return;
                }
                continue;
            }
            body = read_framed_message(&mut read_half) => match body {
                Ok(b) => b,
                Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => {
                    tracing::info!("Client disconnected.");
                    return;
                }
                Err(e) => {
                    tracing::warn!("Read error: {:?}", e);
                    return;
                }
            }
        };

        match fxp::FxpMessage::decode(body.as_slice()) {
            Ok(msg) => match msg.payload {
                Some(fxp::fxp_message::Payload::NewOrder(order)) => {
                    let symbol = order.symbol.clone();
                    let tx = match router.get_or_admit(&symbol).await {
                        Ok(tx) => tx,
                        Err(reason) => {
                            tracing::warn!(
                                order_id = %order.order_id,
                                symbol,
                                side = order.side,
                                sender = %order.sender,
                                reason,
                                "rejecting order during symbol admission"
                            );
                            output_tx.try_send_frame(frame_message(&fxp::FxpMessage {
                                payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                    server_reject(
                                        &order.order_id,
                                        &symbol,
                                        &order.client_order_id,
                                        "",
                                        order.side,
                                        order.quantity,
                                        &order.account,
                                        reason,
                                    ),
                                )),
                            }));
                            continue;
                        }
                    };
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
                    let Some(tx) = router.get_existing(&symbol).await else {
                        let reason = if symbol.is_empty() {
                            "symbol must not be empty"
                        } else {
                            "unknown symbol"
                        };
                        output_tx.try_send_frame(frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                server_reject(
                                    &cancel.order_id,
                                    &symbol,
                                    &cancel.client_cancel_id,
                                    &cancel.order_id,
                                    0,
                                    0,
                                    "",
                                    reason,
                                ),
                            )),
                        }));
                        continue;
                    };
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
                    let Some(tx) = router.get_existing(&symbol).await else {
                        let reason = if symbol.is_empty() {
                            "symbol must not be empty"
                        } else {
                            "unknown symbol"
                        };
                        output_tx.try_send_frame(frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::ExecutionReport(
                                server_reject(
                                    &cr.order_id,
                                    &symbol,
                                    &cr.client_replace_id,
                                    &cr.order_id,
                                    0,
                                    0,
                                    "",
                                    reason,
                                ),
                            )),
                        }));
                        continue;
                    };
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

                Some(fxp::fxp_message::Payload::OrderMassCancel(request)) => {
                    mass_cancel(&router, request, &output_tx).await;
                }

                Some(fxp::fxp_message::Payload::MarketDataRequest(request)) => {
                    tracing::debug!("MarketDataRequest for {:?}", request.symbols);
                    for symbol in &request.symbols {
                        let Some(sym_tx) = router.get_existing(symbol).await else {
                            tracing::warn!(
                                symbol,
                                "ignoring market-data request for unknown symbol"
                            );
                            continue;
                        };
                        let _ = sym_tx
                            .send(SymbolMsg::Subscribe(SubscribeMsg {
                                tx: output_tx.clone(),
                            }))
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

#[allow(clippy::too_many_arguments)]
fn server_reject(
    order_id: &str,
    symbol: &str,
    client_order_id: &str,
    orig_order_id: &str,
    side: i32,
    order_quantity: i32,
    account: &str,
    reject_reason: &str,
) -> fxp::ExecutionReport {
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
    fxp::ExecutionReport {
        header: Some(fxp::FxpHeader {
            protocol_version: crate::PROTOCOL_VERSION.to_string(),
            sequence_number: 0,
        }),
        execution_id: String::new(),
        order_id: order_id.to_string(),
        symbol: symbol.to_string(),
        execution_type: fxp::ExecutionType::ExecRejected as i32,
        order_status: fxp::OrderStatus::OrderRejected as i32,
        execution_price: 0,
        executed_quantity: 0,
        timestamp: Some(prost_types::Timestamp {
            seconds: now.as_secs() as i64,
            nanos: now.subsec_nanos() as i32,
        }),
        leaves_quantity: 0,
        avg_px: 0,
        cum_quantity: 0,
        account: account.to_string(),
        client_order_id: client_order_id.to_string(),
        orig_order_id: orig_order_id.to_string(),
        reject_reason: reject_reason.to_string(),
        side,
        order_quantity,
        auction: false,
    }
}

/// Fans an owner-scoped mass cancel out to the requested symbol (or every
/// symbol) and answers with one OrderMassCancelReport after all symbols have
/// sent their EXEC_CANCELED reports.
async fn mass_cancel(
    router: &SymbolRouter,
    request: fxp::OrderMassCancelRequest,
    output_tx: &OutputTx,
) {
    let mut reject = if request.sender.is_empty() {
        "mass cancel requires a sender"
    } else if fxp::OrderSide::try_from(request.side).is_err() {
        "unsupported side"
    } else if fxp::MassCancelReason::try_from(request.reason).is_err() {
        "unsupported reason"
    } else {
        ""
    };
    let mut replies = Vec::new();
    if reject.is_empty() {
        let symbols = if request.symbol.is_empty() {
            router.symbols().await
        } else {
            vec![request.symbol.clone()]
        };
        for symbol in symbols {
            let (reply, done) = tokio::sync::oneshot::channel();
            let Some(tx) = router.get_existing(&symbol).await else {
                reject = "unknown symbol";
                break;
            };
            let msg = SymbolMsg::MassCancel(MassCancelMsg {
                request: request.clone(),
                output_tx: output_tx.clone(),
                reply: Some(reply),
            });
            if tx.send(msg).await.is_ok() {
                replies.push(done);
            } else {
                tracing::error!("Symbol task for {} dropped on mass cancel", symbol);
            }
        }
    }
    let out = output_tx.clone();
    tokio::spawn(async move {
        let mut total_affected = 0;
        for done in replies {
            total_affected += done.await.unwrap_or(0);
        }
        let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
        let report = fxp::OrderMassCancelReport {
            header: None,
            client_request_id: request.client_request_id,
            sender: request.sender,
            symbol: request.symbol,
            side: request.side,
            total_affected,
            reason: request.reason,
            reject_reason: reject.into(),
            timestamp: Some(prost_types::Timestamp {
                seconds: now.as_secs() as i64,
                nanos: now.subsec_nanos() as i32,
            }),
        };
        out.try_send_frame(frame_message(&fxp::FxpMessage {
            payload: Some(fxp::fxp_message::Payload::OrderMassCancelReport(report)),
        }));
    });
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

pub async fn start_server(shutdown: impl Future<Output = ()> + Send) -> Result<(), String> {
    let schedule = match std::env::var("FXP_SESSION_SCHEDULE") {
        Ok(value) => parse_session_schedule(Some(&value)),
        Err(std::env::VarError::NotPresent) => parse_session_schedule(None),
        Err(error) => Err(error.to_string()),
    }
    .unwrap_or_else(|error| {
        tracing::error!("invalid FXP_SESSION_SCHEDULE: {error}");
        std::process::exit(1);
    });
    let stp_default = StpMode::parse(&std::env::var("FXP_STP_DEFAULT").unwrap_or_default())
        .unwrap_or_else(|error| {
            tracing::error!("invalid FXP_STP_DEFAULT: {error}");
            std::process::exit(1);
        });
    let allowed_symbols = read_optional_env("FXP_SYMBOLS").unwrap_or_else(|error| {
        tracing::error!("{error}");
        std::process::exit(1);
    });
    let max_symbols = read_optional_env("FXP_MAX_SYMBOLS").unwrap_or_else(|error| {
        tracing::error!("{error}");
        std::process::exit(1);
    });
    let (allowed_symbols, max_symbols) =
        parse_symbol_admission(allowed_symbols.as_deref(), max_symbols.as_deref()).unwrap_or_else(
            |error| {
                tracing::error!("invalid symbol admission configuration: {error}");
                std::process::exit(1);
            },
        );
    let mut router = SymbolRouter::new(schedule);
    router.stp_default = stp_default;
    router.allowed_symbols = allowed_symbols.map(Arc::new);
    router.max_symbols = max_symbols;
    if let Some(path) = std::env::var_os("FXP_EVENT_LOG") {
        use crate::engine::Clock;
        if path.is_empty() {
            persistence_failure("FXP_EVENT_LOG must be a nonempty path");
        }
        let (detached, frame_rx, md_rx) = ClientTx::channel(1, 1);
        drop(frame_rx);
        drop(md_rx);
        let mut journal =
            Journal::open(std::path::Path::new(&path), schedule, stp_default, detached)
                .unwrap_or_else(|error| persistence_failure(error));
        let symbols = journal.symbols();
        let at = WallClock.now();
        for symbol in &symbols {
            journal
                .tick(symbol, at)
                .unwrap_or_else(|error| persistence_failure(error));
        }
        router.persistence = Some(Arc::new(std::sync::Mutex::new(journal)));
        for symbol in symbols {
            router.restore_symbol(&symbol).await;
        }
    }
    let listener = TcpListener::bind("127.0.0.1:8080").await.unwrap();

    tracing::info!("FXP Core running on 127.0.0.1:8080");
    tracing::info!("Architecture: per-symbol tasks + lock-free output channels");
    run_server_with_router(listener, router, shutdown, shutdown_timeout()).await
}

pub async fn run_server(
    listener: TcpListener,
    shutdown: impl Future<Output = ()> + Send,
) -> Result<(), String> {
    run_server_with_router(
        listener,
        SymbolRouter::new(None),
        shutdown,
        shutdown_timeout(),
    )
    .await
}

fn shutdown_timeout() -> Duration {
    let value = read_optional_env("FXP_SHUTDOWN_TIMEOUT_SEC").unwrap_or_else(|error| {
        tracing::error!("{error}");
        std::process::exit(1);
    });
    match value {
        None => Duration::from_secs(5),
        Some(value) => match value.parse::<u64>() {
            Ok(seconds) if seconds > 0 => Duration::from_secs(seconds),
            _ => {
                tracing::error!("FXP_SHUTDOWN_TIMEOUT_SEC must be a positive integer");
                std::process::exit(1);
            }
        },
    }
}

async fn run_server_with_router<F>(
    listener: TcpListener,
    router: SymbolRouter,
    shutdown: F,
    timeout: Duration,
) -> Result<(), String>
where
    F: Future<Output = ()> + Send,
{
    let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
    let (writer_drain_tx, writer_drain_rx) = tokio::sync::watch::channel(false);
    let (writer_tx, mut writer_rx) = mpsc::unbounded_channel();
    let mut handlers = tokio::task::JoinSet::new();
    tokio::pin!(shutdown);
    let mut backoff = ACCEPT_BACKOFF_MIN;
    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => break,
            accepted = accept_client(&listener) => match accepted {
                Ok(stream) => {
                    backoff = ACCEPT_BACKOFF_MIN;
                    let router_clone = router.clone();
                    let shutdown = shutdown_rx.clone();
                    let writer_drain = writer_drain_rx.clone();
                    let writers = writer_tx.clone();
                    handlers.spawn(async move {
                        handle_tcp_client_with_shutdown(
                            stream,
                            router_clone,
                            shutdown,
                            writer_drain,
                            writers,
                        )
                        .await;
                    });
                }
                // Transient errors (EMFILE, ECONNABORTED, ...) do not kill the server.
                Err(e) => {
                    tracing::error!("accept failed: {e}; retrying in {backoff:?}");
                    tokio::time::sleep(backoff).await;
                    backoff = next_accept_backoff(backoff);
                }
            }
        }
    }

    drop(listener);
    let _ = shutdown_tx.send(true);
    drop(writer_tx);
    let drain = async {
        while let Some(result) = handlers.join_next().await {
            if let Err(error) = result {
                tracing::error!("client task failed during shutdown: {error}");
            }
        }
        for task in router.stop_symbols().await {
            if let Err(error) = task.await {
                tracing::error!("symbol task failed during shutdown: {error}");
            }
        }
        let _ = writer_drain_tx.send(true);
        let mut writers = Vec::new();
        while let Some(writer) = writer_rx.recv().await {
            writers.push(writer);
        }
        for writer in writers {
            if let Err(error) = writer.await {
                tracing::error!("client writer failed during shutdown: {error}");
            }
        }
        if let Some(persistence) = &router.persistence {
            persistence
                .lock()
                .unwrap_or_else(|error| persistence_failure(error))
                .sync()
                .unwrap_or_else(|error| persistence_failure(error));
        }
    };
    match tokio::time::timeout(timeout, drain).await {
        Ok(()) => {
            tracing::info!("core shutdown completed");
            Ok(())
        }
        Err(_) => {
            tracing::error!("core shutdown timed out; abandoning unfinished work");
            Err("core shutdown timed out".into())
        }
    }
}

fn parse_symbol_admission(
    allowed_symbols: Option<&str>,
    max_symbols: Option<&str>,
) -> Result<(Option<HashSet<String>>, Option<usize>), String> {
    let allowed_symbols = allowed_symbols
        .map(|value| {
            let symbols: HashSet<_> = value
                .split(',')
                .map(str::trim)
                .filter(|symbol| !symbol.is_empty())
                .map(str::to_string)
                .collect();
            if symbols.is_empty() {
                Err("FXP_SYMBOLS must contain at least one nonempty symbol".to_string())
            } else {
                Ok(symbols)
            }
        })
        .transpose()?;
    let max_symbols = max_symbols
        .map(|value| {
            let limit = value
                .parse::<usize>()
                .map_err(|_| "FXP_MAX_SYMBOLS must be a positive integer".to_string())?;
            if limit == 0 {
                return Err("FXP_MAX_SYMBOLS must be greater than zero".to_string());
            }
            Ok(limit)
        })
        .transpose()?;
    Ok((allowed_symbols, max_symbols))
}

fn read_optional_env(name: &str) -> Result<Option<String>, String> {
    match std::env::var(name) {
        Ok(value) => Ok(Some(value)),
        Err(std::env::VarError::NotPresent) => Ok(None),
        Err(error) => Err(format!("invalid {name}: {error}")),
    }
}

#[cfg(test)]
mod symbol_admission_tests {
    use super::*;
    use std::time::Duration;

    async fn round_trip(
        router: &SymbolRouter,
        payload: fxp::fxp_message::Payload,
    ) -> fxp::fxp_message::Payload {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let client = tokio::net::TcpStream::connect(listener.local_addr().unwrap())
            .await
            .unwrap();
        let server = accept_client(&listener).await.unwrap();
        let handler = tokio::spawn(handle_tcp_client(server, router.clone()));
        let (mut read, mut write) = client.into_split();
        write
            .write_all(&frame_message(&fxp::FxpMessage {
                payload: Some(payload),
            }))
            .await
            .unwrap();
        let body = read_framed_message(&mut read).await.unwrap();
        drop(write);
        tokio::time::timeout(Duration::from_secs(2), handler)
            .await
            .unwrap()
            .unwrap();
        fxp::FxpMessage::decode(body.as_slice())
            .unwrap()
            .payload
            .unwrap()
    }

    #[tokio::test]
    async fn max_symbols_rejects_new_symbols_without_starting_tasks() {
        let mut router = SymbolRouter::new(None);
        router.max_symbols = Some(2);
        router.get_or_admit("A").await.unwrap();
        router.get_or_admit("B").await.unwrap();
        assert_eq!(
            router.get_or_admit("C").await.unwrap_err(),
            "maximum symbol count reached"
        );
        assert_eq!(router.symbols().await, ["A", "B"]);
        assert_eq!(router.channels.lock().await.len(), 2);
    }

    #[tokio::test]
    async fn third_new_order_is_rejected_without_creating_a_symbol_task() {
        let mut router = SymbolRouter::new(None);
        router.max_symbols = Some(2);
        for symbol in ["A", "B"] {
            round_trip(
                &router,
                fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
                    order_id: format!("O{symbol}"),
                    sender: "T".into(),
                    symbol: symbol.into(),
                    side: 0,
                    quantity: 1,
                    ..Default::default()
                }),
            )
            .await;
        }
        let response = round_trip(
            &router,
            fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
                order_id: "OC".into(),
                sender: "T".into(),
                symbol: "C".into(),
                side: fxp::OrderSide::Buy as i32,
                quantity: 1,
                ..Default::default()
            }),
        )
        .await;
        let fxp::fxp_message::Payload::ExecutionReport(report) = response else {
            panic!("expected admission rejection");
        };
        assert_eq!(report.reject_reason, "maximum symbol count reached");
        assert_eq!(report.order_id, "OC");
        assert_eq!(report.symbol, "C");
        assert_eq!(report.side, fxp::OrderSide::Buy as i32);
        assert!(report.execution_id.is_empty());
        assert_eq!(router.symbols().await, ["A", "B"]);
        assert_eq!(router.channels.lock().await.len(), 2);
    }

    #[tokio::test]
    async fn allowlist_rejects_nonlisted_symbols() {
        let mut router = SymbolRouter::new(None);
        router.allowed_symbols = Some(Arc::new(HashSet::from(["ALLOWED".to_string()])));
        router.get_or_admit("ALLOWED").await.unwrap();
        assert_eq!(
            router.get_or_admit("OTHER").await.unwrap_err(),
            "symbol is not allowed"
        );
        assert_eq!(router.symbols().await, ["ALLOWED"]);
    }

    #[tokio::test]
    async fn unknown_cancel_replace_and_mass_cancel_reject_without_admission() {
        let router = SymbolRouter::new(None);
        for (payload, request_id) in [
            (
                fxp::fxp_message::Payload::OrderCancel(fxp::OrderCancelRequest {
                    order_id: "O1".into(),
                    client_cancel_id: "C1".into(),
                    sender: "T".into(),
                    symbol: "UNKNOWN".into(),
                    ..Default::default()
                }),
                "C1",
            ),
            (
                fxp::fxp_message::Payload::OrderCancelReplace(fxp::OrderCancelReplaceRequest {
                    order_id: "O2".into(),
                    client_replace_id: "R2".into(),
                    sender: "T".into(),
                    symbol: "UNKNOWN".into(),
                    ..Default::default()
                }),
                "R2",
            ),
        ] {
            let fxp::fxp_message::Payload::ExecutionReport(report) =
                round_trip(&router, payload).await
            else {
                panic!("expected cancel/replace rejection");
            };
            assert_eq!(report.reject_reason, "unknown symbol");
            assert_eq!(report.client_order_id, request_id);
            assert_eq!(report.orig_order_id, report.order_id);
            assert!(report.execution_id.is_empty());
        }

        let (output_tx, mut frames, _md) = ClientTx::channel(64, 64);
        mass_cancel(
            &router,
            fxp::OrderMassCancelRequest {
                sender: "T".into(),
                symbol: "UNKNOWN".into(),
                ..Default::default()
            },
            &output_tx,
        )
        .await;
        let frame = frames.recv().await.unwrap();
        let fxp::fxp_message::Payload::OrderMassCancelReport(report) =
            fxp::FxpMessage::decode(&frame[4..])
                .unwrap()
                .payload
                .unwrap()
        else {
            panic!("expected mass-cancel rejection");
        };
        assert_eq!(report.reject_reason, "unknown symbol");
        assert!(router.symbols().await.is_empty());
    }

    #[tokio::test]
    async fn unknown_commands_do_not_create_symbols() {
        let router = SymbolRouter::new(None);
        for _ in ["cancel", "replace", "mass-cancel", "market-data"] {
            assert!(router.get_existing("UNKNOWN").await.is_none());
        }
        assert!(router.symbols().await.is_empty());
        assert_eq!(router.channels.lock().await.len(), 0);
    }

    #[tokio::test]
    async fn persisted_symbols_are_restored_outside_admission_limits() {
        let mut router = SymbolRouter::new(None);
        router.allowed_symbols = Some(Arc::new(HashSet::from(["ALLOWED".to_string()])));
        router.max_symbols = Some(1);
        router.restore_symbol("PERSISTED").await;
        assert_eq!(router.symbols().await, ["PERSISTED"]);
    }

    #[test]
    fn parses_symbol_admission_settings() {
        assert_eq!(parse_symbol_admission(None, None).unwrap(), (None, None));
        let (symbols, maximum) = parse_symbol_admission(Some(" A, B ,A "), Some("2")).unwrap();
        assert_eq!(symbols.unwrap(), HashSet::from(["A".into(), "B".into()]));
        assert_eq!(maximum, Some(2));
        assert!(parse_symbol_admission(Some(" , "), None).is_err());
        assert!(parse_symbol_admission(None, Some("0")).is_err());
        assert!(parse_symbol_admission(None, Some("bad")).is_err());
    }

    #[test]
    fn admission_reject_has_engine_reject_fields_without_exec_id() {
        let report = server_reject("O1", "S", "C1", "O1", 0, 0, "", "unknown symbol");
        assert_eq!(
            (
                report.execution_type,
                report.order_status,
                report.order_id.as_str(),
                report.symbol.as_str(),
                report.client_order_id.as_str(),
                report.orig_order_id.as_str(),
                report.reject_reason.as_str(),
                report.execution_id.as_str(),
            ),
            (
                fxp::ExecutionType::ExecRejected as i32,
                fxp::OrderStatus::OrderRejected as i32,
                "O1",
                "S",
                "C1",
                "O1",
                "unknown symbol",
                "",
            )
        );
    }
}

fn parse_session_schedule(value: Option<&str>) -> Result<Option<SessionSchedule>, String> {
    value.map(str::parse).transpose()
}

#[cfg(test)]
mod schedule_tests {
    use super::*;

    #[test]
    fn optional_schedule_config() {
        assert_eq!(parse_session_schedule(None).unwrap(), None);
        assert!(
            parse_session_schedule(Some("close=16:00:00"))
                .unwrap()
                .is_some()
        );
        for garbage in ["", "close=24:00", "close=16:00,garbage=1"] {
            assert!(parse_session_schedule(Some(garbage)).is_err());
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
        let (tx, rx, md_rx) = conn_with_channels(64, 64);
        drop(md_rx);
        (tx, rx)
    }

    fn conn_with_channels(frame_capacity: usize, md_capacity: usize) -> (OutputTx, Rx, Rx) {
        ClientTx::channel(frame_capacity, md_capacity)
    }

    #[tokio::test]
    async fn resubscribing_one_channel_streams_market_data_once() {
        let book = spawn_book();
        let (md_tx, _frame_rx, mut md_rx) = conn_with_channels(64, 64);
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

    #[tokio::test]
    async fn slow_market_data_subscriber_does_not_block_other_orders() {
        let book = spawn_book();
        let (md_tx, _frame_rx, _md_rx) = conn_with_channels(64, 1);
        let counter = md_tx.clone();
        book.send(SymbolMsg::Subscribe(SubscribeMsg { tx: md_tx }))
            .await
            .unwrap();
        let (seller, mut seller_rx) = conn();
        let (buyer, mut buyer_rx) = conn();
        for i in 0..25 {
            send_order(
                &book,
                nos(&format!("S{i}"), "S", SELL, 10_000, 1, LIMIT, GTC),
                &seller,
            )
            .await;
            send_order(
                &book,
                nos(&format!("B{i}"), "B", BUY, 10_000, 1, LIMIT, DAY),
                &buyer,
            )
            .await;
        }

        tokio::time::timeout(Duration::from_secs(1), async {
            for _ in 0..50 {
                next(&mut seller_rx).await;
                next(&mut buyer_rx).await;
            }
        })
        .await
        .expect("slow market-data consumer stalled execution reports");
        assert!(counter.md_dropped.load(AtomicOrdering::Relaxed) > 0);
    }

    #[tokio::test]
    async fn slow_resting_owner_does_not_block_aggressor_reports() {
        let book = spawn_book();
        let (owner_tx, _owner_rx, _owner_md_rx) = conn_with_channels(1, 64);
        let (aggressor_tx, mut aggressor_rx) = conn();
        send_order(
            &book,
            nos("SLOW", "S", SELL, 10_000, 10, LIMIT, GTC),
            &owner_tx,
        )
        .await;

        for id in ["FAST1", "FAST2"] {
            send_order(
                &book,
                nos(id, "B", BUY, 10_000, 1, LIMIT, DAY),
                &aggressor_tx,
            )
            .await;
        }
        tokio::time::timeout(Duration::from_secs(1), async {
            for _ in 0..4 {
                next(&mut aggressor_rx).await;
            }
        })
        .await
        .expect("slow resting owner stalled aggressor execution reports");
        tokio::time::timeout(Duration::from_secs(1), owner_tx.slow.notified())
            .await
            .expect("slow resting owner did not receive a slow-consumer signal");
    }

    #[tokio::test]
    async fn tcp_slow_consumer_probe_keeps_healthy_order_latency_bounded() {
        use tokio::net::{TcpSocket, TcpStream};
        use tokio::time::Instant;

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let router = SymbolRouter::new(None);

        let slow_socket = TcpSocket::new_v4().unwrap();
        slow_socket.set_recv_buffer_size(1024).unwrap();
        let mut slow_client = slow_socket.connect(addr).await.unwrap();
        let slow_server = accept_client(&listener).await.unwrap();
        let slow_handler = tokio::spawn(handle_tcp_client(slow_server, router.clone()));

        let subscribe = fxp::FxpMessage {
            payload: Some(fxp::fxp_message::Payload::MarketDataRequest(
                fxp::MarketDataRequest {
                    symbols: vec!["STALL".into()],
                    ..Default::default()
                },
            )),
        };
        let mut resting = nos("REST", "S", SELL, 10_000, 100, LIMIT, GTC);
        resting.symbol = "STALL".into();
        slow_client
            .write_all(&frame_message(&subscribe))
            .await
            .unwrap();
        slow_client
            .write_all(&frame_message(&fxp::FxpMessage {
                payload: Some(fxp::fxp_message::Payload::NewOrder(resting)),
            }))
            .await
            .unwrap();

        let mut flood = Vec::new();
        for i in 0..100_000 {
            let mut order = nos(&format!("BAD{i}"), "S", 0, 10_000, 1, LIMIT, GTC);
            order.symbol = "STALL".into();
            flood.extend_from_slice(&frame_message(&fxp::FxpMessage {
                payload: Some(fxp::fxp_message::Payload::NewOrder(order)),
            }));
        }
        let flood_task = tokio::spawn(async move {
            let _ = slow_client.write_all(&flood).await;
            tokio::time::timeout(Duration::from_secs(15), async {
                let mut bytes = [0u8; 8192];
                loop {
                    match slow_client.read(&mut bytes).await {
                        Ok(0) | Err(_) => break true,
                        Ok(_) => {}
                    }
                }
            })
            .await
        });

        let mut healthy_client = TcpStream::connect(addr).await.unwrap();
        let healthy_server = accept_client(&listener).await.unwrap();
        let healthy_handler = tokio::spawn(handle_tcp_client(healthy_server, router));
        let mut latencies = Vec::with_capacity(2_000);
        let total_start = Instant::now();
        for i in 0..2_000 {
            let mut order = nos(&format!("FAST{i}"), "B", 0, 10_000, 1, LIMIT, GTC);
            order.symbol = "FAST".into();
            let frame = frame_message(&fxp::FxpMessage {
                payload: Some(fxp::fxp_message::Payload::NewOrder(order)),
            });
            let start = Instant::now();
            healthy_client.write_all(&frame).await.unwrap();
            let mut len = [0u8; 4];
            healthy_client.read_exact(&mut len).await.unwrap();
            let mut body = vec![0; u32::from_be_bytes(len) as usize];
            healthy_client.read_exact(&mut body).await.unwrap();
            assert!(matches!(
                fxp::FxpMessage::decode(body.as_slice()).unwrap().payload,
                Some(fxp::fxp_message::Payload::ExecutionReport(_))
            ));
            latencies.push(start.elapsed());
        }
        let total = total_start.elapsed();
        latencies.sort_unstable();
        let p50 = latencies[latencies.len() / 2];
        let p99 = latencies[(latencies.len() * 99 - 1) / 100];
        println!(
            "slow-consumer probe: orders=2000 p50_us={} p99_us={} total_ms={}",
            p50.as_micros(),
            p99.as_micros(),
            total.as_millis()
        );
        assert!(p99 < Duration::from_millis(50), "p99 latency was {p99:?}");
        assert!(total < Duration::from_secs(30), "probe took {total:?}");

        let disconnect = flood_task.await.unwrap();
        assert!(
            matches!(disconnect, Ok(true)),
            "stalled client was not disconnected: {disconnect:?}"
        );
        tokio::time::timeout(Duration::from_secs(2), slow_handler)
            .await
            .expect("slow-consumer handler did not return")
            .unwrap();
        healthy_client.shutdown().await.unwrap();
        tokio::time::timeout(Duration::from_secs(2), healthy_handler)
            .await
            .expect("healthy handler did not return")
            .unwrap();
    }

    #[tokio::test]
    async fn router_contention_socket_probe_reports_three_runs() {
        use tokio::net::TcpStream;
        use tokio::time::Instant;

        const CONNECTIONS: usize = 8;
        const SYMBOLS: usize = 25;
        const ORDERS_PER_CONNECTION: usize = 1_000;

        for run in 1..=3 {
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let address = listener.local_addr().unwrap();
            let router = SymbolRouter::new(None);
            let mut clients = Vec::with_capacity(CONNECTIONS);
            let mut handlers = Vec::with_capacity(CONNECTIONS);
            for _ in 0..CONNECTIONS {
                let client = TcpStream::connect(address).await.unwrap();
                let server = accept_client(&listener).await.unwrap();
                handlers.push(tokio::spawn(handle_tcp_client(server, router.clone())));
                clients.push(client);
            }

            let total_start = Instant::now();
            let mut tasks = tokio::task::JoinSet::new();
            for (connection, mut client) in clients.into_iter().enumerate() {
                tasks.spawn(async move {
                    let mut latencies = Vec::with_capacity(ORDERS_PER_CONNECTION);
                    for index in 0..ORDERS_PER_CONNECTION {
                        let mut order = nos(
                            &format!("R{run}-C{connection}-O{index}"),
                            &format!("T{connection}"),
                            0,
                            10_000,
                            1,
                            LIMIT,
                            GTC,
                        );
                        order.symbol = format!("PROBE{}", index % SYMBOLS);
                        let frame = frame_message(&fxp::FxpMessage {
                            payload: Some(fxp::fxp_message::Payload::NewOrder(order)),
                        });
                        let start = Instant::now();
                        client.write_all(&frame).await.unwrap();
                        let mut length = [0u8; 4];
                        tokio::time::timeout(
                            Duration::from_secs(5),
                            client.read_exact(&mut length),
                        )
                        .await
                        .expect("timed out reading router-probe response")
                        .unwrap();
                        let mut body = vec![0; u32::from_be_bytes(length) as usize];
                        client.read_exact(&mut body).await.unwrap();
                        assert!(matches!(
                            fxp::FxpMessage::decode(body.as_slice()).unwrap().payload,
                            Some(fxp::fxp_message::Payload::ExecutionReport(_))
                        ));
                        latencies.push(start.elapsed());
                    }
                    latencies
                });
            }

            let mut latencies = Vec::with_capacity(CONNECTIONS * ORDERS_PER_CONNECTION);
            while let Some(result) = tasks.join_next().await {
                latencies.extend(result.unwrap());
            }
            let total = total_start.elapsed();
            latencies.sort_unstable();
            let p50 = latencies[latencies.len() / 2];
            let p99 = latencies[(latencies.len() * 99 - 1) / 100];
            let orders_per_second = latencies.len() as f64 / total.as_secs_f64();
            println!(
                "router probe run={run} connections={CONNECTIONS} symbols={SYMBOLS} orders={} orders_per_sec={orders_per_second:.0} p50_us={} p99_us={} total_ms={}",
                latencies.len(),
                p50.as_micros(),
                p99.as_micros(),
                total.as_millis()
            );
            for handler in handlers {
                tokio::time::timeout(Duration::from_secs(2), handler)
                    .await
                    .expect("router-probe handler did not exit")
                    .unwrap();
            }
        }
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

    async fn next_mass_report(rx: &mut Rx) -> fxp::OrderMassCancelReport {
        let frame = tokio::time::timeout(Duration::from_secs(2), rx.recv())
            .await
            .expect("timed out waiting for mass cancel report")
            .expect("channel closed");
        match fxp::FxpMessage::decode(&frame[4..]).unwrap().payload {
            Some(fxp::fxp_message::Payload::OrderMassCancelReport(r)) => r,
            other => panic!("expected OrderMassCancelReport, got {:?}", other),
        }
    }

    #[tokio::test]
    async fn mass_cancel_fans_out_across_symbols_and_reports_after_the_cancels() {
        let router = SymbolRouter::new(None);
        let (a, mut a_rx) = conn();
        for (id, owner, symbol) in [("A1", "T", "AAA"), ("B1", "T", "BBB"), ("U1", "U", "AAA")] {
            let tx = router.get_or_admit(symbol).await.unwrap();
            let order = fxp::NewOrderSingle {
                symbol: symbol.into(),
                ..nos(id, owner, BUY, 100, 5, LIMIT, GTC)
            };
            send_order(&tx, order, &a).await;
            next(&mut a_rx).await;
        }
        let request = fxp::OrderMassCancelRequest {
            sender: "T".into(),
            client_request_id: "MC".into(),
            ..Default::default()
        };
        mass_cancel(&router, request, &a).await;
        let mut ids = vec![
            next(&mut a_rx).await.order_id,
            next(&mut a_rx).await.order_id,
        ];
        ids.sort();
        assert_eq!(ids, ["A1", "B1"]);
        let report = next_mass_report(&mut a_rx).await;
        assert_eq!(
            (report.client_request_id.as_str(), report.total_affected),
            ("MC", 2)
        );
        assert!(report.reject_reason.is_empty());
        assert_quiet(&mut a_rx).await;
    }

    #[tokio::test]
    async fn mass_cancel_rejects_invalid_requests_without_canceling() {
        let router = SymbolRouter::new(None);
        let (a, mut a_rx) = conn();
        let tx = router.get_or_admit("TEST").await.unwrap();
        send_order(&tx, nos("A1", "T", BUY, 100, 5, LIMIT, GTC), &a).await;
        next(&mut a_rx).await;
        for (request, reason) in [
            (
                fxp::OrderMassCancelRequest {
                    sender: "T".into(),
                    side: 9,
                    ..Default::default()
                },
                "unsupported side",
            ),
            (
                fxp::OrderMassCancelRequest::default(),
                "mass cancel requires a sender",
            ),
        ] {
            mass_cancel(&router, request, &a).await;
            let report = next_mass_report(&mut a_rx).await;
            assert_eq!(
                (report.total_affected, report.reject_reason.as_str()),
                (0, reason)
            );
        }
        assert_quiet(&mut a_rx).await;
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
                stp: Default::default(),
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

#[cfg(test)]
mod shutdown_tests {
    use super::*;
    use crate::event_log::{Journal, tests::TestDir};
    use crate::order_book::order::StpMode;

    #[tokio::test]
    async fn run_server_stops_handlers_and_listener_after_shutdown() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(run_server(listener, async move {
            let _ = shutdown_rx.await;
        }));
        let _client = tokio::net::TcpStream::connect(address).await.unwrap();
        let _ = shutdown_tx.send(());
        tokio::time::timeout(Duration::from_secs(2), server)
            .await
            .expect("server shutdown timed out")
            .expect("server task panicked")
            .expect("server shutdown failed");
        assert!(tokio::net::TcpStream::connect(address).await.is_err());
    }

    #[tokio::test]
    async fn persisted_resting_order_does_not_hold_writer_open_during_shutdown() {
        use tokio::io::AsyncReadExt;
        use tokio::time::Instant;

        let dir = TestDir::new();
        let path = dir.log();
        let detached = ClientTx::channel(1, 1).0;
        let journal = Journal::open(&path, None, StpMode::None, detached).unwrap();
        let mut router = SymbolRouter::new(None);
        router.persistence = Some(Arc::new(std::sync::Mutex::new(journal)));

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(run_server_with_router(
            listener,
            router.clone(),
            async move {
                let _ = shutdown_rx.await;
            },
            Duration::from_secs(2),
        ));
        let mut client = tokio::net::TcpStream::connect(address).await.unwrap();
        let symbol = "PERSIST-SHUTDOWN";
        let order = fxp::FxpMessage {
            payload: Some(fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
                symbol: symbol.into(),
                sender: "owner".into(),
                order_id: "resting-gtc".into(),
                price: 12_345,
                quantity: 7,
                side: 1,
                order_type: 2,
                time_in_force: 2,
                ..Default::default()
            })),
        };
        client.write_all(&frame_message(&order)).await.unwrap();
        let mut length = [0; 4];
        client.read_exact(&mut length).await.unwrap();
        let mut body = vec![0; u32::from_be_bytes(length) as usize];
        client.read_exact(&mut body).await.unwrap();
        let ack = fxp::FxpMessage::decode(body.as_slice()).unwrap();
        let Some(fxp::fxp_message::Payload::ExecutionReport(ack)) = ack.payload else {
            panic!("resting order did not receive an execution report");
        };
        assert_eq!(ack.execution_type, fxp::ExecutionType::ExecNew as i32);

        let started = Instant::now();
        let _ = shutdown_tx.send(());
        let result = tokio::time::timeout(Duration::from_secs(3), server)
            .await
            .expect("server shutdown hung")
            .expect("server task panicked");
        assert!(result.is_ok(), "server shutdown failed: {result:?}");
        assert!(started.elapsed() < Duration::from_secs(2));

        let eof = tokio::time::timeout(Duration::from_secs(1), client.read(&mut [0; 1]))
            .await
            .expect("client did not observe EOF")
            .unwrap();
        assert_eq!(eof, 0);

        drop(router);
        let recovered_client = ClientTx::channel(1, 1).0;
        let mut recovered =
            Journal::open(&path, None, StpMode::None, recovered_client.clone()).unwrap();
        assert_eq!(recovered.symbols(), vec![symbol]);
        let outputs = recovered
            .engines
            .get_mut(symbol)
            .unwrap()
            .cancel(crate::engine::CancelMsg {
                order_id: "resting-gtc".into(),
                sender: "owner".into(),
                client_cancel_id: "cancel-check".into(),
                output_tx: recovered_client,
            });
        assert!(
            outputs.iter().any(|output| matches!(
                output,
                crate::engine::Output::ExecutionReport(_, report)
                    if report.execution_type == fxp::ExecutionType::ExecCanceled as i32
            )),
            "resting order was not restored from the journal"
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn shutdown_drains_reports_after_symbol_tasks_finish() {
        use tokio::io::AsyncReadExt;

        const ORDER_COUNT: usize = 200;

        let mut router = SymbolRouter::new(None);
        let symbol_gate = Arc::new(tokio::sync::Semaphore::new(0));
        router.test_symbol_gate = Some(symbol_gate.clone());

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(run_server_with_router(
            listener,
            router.clone(),
            async move {
                let _ = shutdown_rx.await;
            },
            Duration::from_secs(2),
        ));
        let mut client = tokio::net::TcpStream::connect(address).await.unwrap();
        for index in 0..ORDER_COUNT {
            let order = fxp::FxpMessage {
                payload: Some(fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
                    symbol: "SHUTDOWN-DRAIN".into(),
                    sender: "owner".into(),
                    order_id: format!("resting-{index}"),
                    price: 12_345,
                    quantity: 7,
                    side: 1,
                    order_type: 2,
                    time_in_force: 2,
                    ..Default::default()
                })),
            };
            client.write_all(&frame_message(&order)).await.unwrap();
        }

        let queued_sender = tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                if let Some(sender) = router.get_existing("SHUTDOWN-DRAIN").await
                    && sender.capacity() == sender.max_capacity() - ORDER_COUNT
                {
                    break sender;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("symbol task did not queue all resting orders");

        drop(queued_sender);
        let _ = shutdown_tx.send(());
        let mut probe = [0; 1];
        let writer_closed_early =
            match tokio::time::timeout(Duration::from_millis(100), client.read(&mut probe)).await {
                Ok(Ok(0)) | Ok(Err(_)) => true,
                Ok(Ok(_)) => false,
                Err(_) => false,
            };
        symbol_gate.add_permits(1);

        tokio::time::timeout(Duration::from_secs(3), server)
            .await
            .expect("server shutdown hung")
            .expect("server task panicked")
            .expect("server shutdown failed");

        for index in 0..ORDER_COUNT {
            let mut length = [0; 4];
            tokio::time::timeout(Duration::from_secs(1), client.read_exact(&mut length))
                .await
                .unwrap_or_else(|_| panic!("timed out waiting for report {index}"))
                .unwrap_or_else(|error| panic!("missing report {index}: {error}"));
            let mut body = vec![0; u32::from_be_bytes(length) as usize];
            client.read_exact(&mut body).await.unwrap();
            let response = fxp::FxpMessage::decode(body.as_slice()).unwrap();
            let Some(fxp::fxp_message::Payload::ExecutionReport(report)) = response.payload else {
                panic!("response {index} was not an ExecutionReport");
            };
            assert_eq!(report.order_id, format!("resting-{index}"));
            assert_eq!(report.execution_type, fxp::ExecutionType::ExecNew as i32);
        }

        assert!(
            !writer_closed_early,
            "client writer closed before symbol tasks finished"
        );
        let eof = tokio::time::timeout(Duration::from_secs(1), client.read(&mut [0; 1]))
            .await
            .expect("client did not observe EOF")
            .unwrap();
        assert_eq!(eof, 0);
    }
}
