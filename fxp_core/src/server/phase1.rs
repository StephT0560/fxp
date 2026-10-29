// SPDX-License-Identifier: MIT
//! Isolated transport work, no matching/time/throughput measurement.
use super::*;
#[global_allocator]
static ALLOCATOR: dhat::Alloc = dhat::Alloc;
pub(super) static ENCODES: AtomicU64 = AtomicU64::new(0);

#[test]
#[ignore = "isolated dhat process; run through scripts/phase1_lite.py"]
fn core_attribution() {
    let n: usize = std::env::var("FXP_PHASE1_SUBSCRIBERS")
        .unwrap()
        .parse()
        .unwrap();
    let path = std::env::var("FXP_PHASE1_OUTPUT").unwrap();
    let _profiler = dhat::Profiler::builder()
        .file_name(format!("{path}.dhat.json"))
        .build();
    let mut subscribers = Vec::new();
    let mut receivers = Vec::new();
    for _ in 0..n {
        let (tx, _, rx) = ClientTx::channel(1, 32);
        subscribers.push(tx);
        receivers.push(rx);
    }
    let msg = fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
            fxp::MarketDataIncrementalUpdate {
                symbol: "PHASE1".into(),
                changes: vec![fxp::MarketOrderUpdate {
                    update_type: 0,
                    price: 100,
                    quantity: 1,
                    side: 1,
                }],
                ..Default::default()
            },
        )),
    };
    // Warm lazy queue storage before measuring only encode/clone/enqueue/drain.
    for _ in 0..128 {
        broadcast_md(&mut subscribers, frame_message(&msg));
        for rx in &mut receivers {
            rx.try_recv().unwrap();
        }
    }
    ENCODES.store(0, AtomicOrdering::Relaxed);
    let before = dhat::HeapStats::get();
    let mut delivered = 0;
    let mut bytes = 0;
    for _ in 0..2000 {
        broadcast_md(&mut subscribers, frame_message(&msg));
        for rx in &mut receivers {
            bytes += rx.try_recv().unwrap().len();
            delivered += 1;
        }
    }
    let after = dhat::HeapStats::get();
    assert_eq!(ENCODES.load(AtomicOrdering::Relaxed), 2000);
    assert_eq!(delivered, 2000 * n);
    let result = format!(
        "{{\"subscribers\":{n},\"updates\":2000,\"delivered\":{delivered},\"encodes\":2000,\"allocations\":{},\"allocated_bytes\":{},\"delivered_bytes\":{bytes}}}\n",
        after.total_blocks - before.total_blocks,
        after.total_bytes - before.total_bytes
    );
    std::fs::write(path, result).unwrap();
}

#[tokio::test]
#[ignore = "isolated dhat process; run through scripts/phase1_lite.py"]
async fn core_slow_consumer() {
    let path = std::env::var("FXP_PHASE1_OUTPUT").unwrap();
    let _profiler = dhat::Profiler::builder()
        .file_name(format!("{path}.dhat.json"))
        .build();
    let (tx, _, rx) = ClientTx::channel(8, 16);
    let mut subs = vec![tx.clone()];
    let frame = vec![0u8; 128];
    for _ in 0..1000 {
        broadcast_md(&mut subs, frame.clone());
    }
    let before = dhat::HeapStats::get();
    for _ in 0..10000 {
        broadcast_md(&mut subs, frame.clone());
    }
    let after = dhat::HeapStats::get();
    assert_eq!(rx.len(), 16);
    assert_eq!(tx.md_dropped.load(AtomicOrdering::Relaxed), 10984);
    assert_eq!(before.curr_bytes, after.curr_bytes);
    let (book, input) = mpsc::channel(16);
    tokio::spawn(order_book_task("SLOWCHECK".into(), input));
    let (owner, mut reports, _) = ClientTx::channel(16, 16);
    book.send(SymbolMsg::Subscribe(SubscribeMsg { tx: tx.clone() }))
        .await
        .unwrap();
    for i in 0..2000 {
        book.send(SymbolMsg::Order(OrderMsg {
            order: fxp::NewOrderSingle {
                symbol: "SLOWCHECK".into(),
                sender: "owner".into(),
                order_id: format!("Q{i}"),
                price: 100,
                side: 1,
                quantity: 1,
                order_type: 2,
                time_in_force: 2,
                ..Default::default()
            },
            output_tx: owner.clone(),
        }))
        .await
        .unwrap();
        tokio::time::timeout(std::time::Duration::from_secs(5), reports.recv())
            .await
            .unwrap()
            .unwrap();
    }
    std::fs::write(path,format!("{{\"queue_depth\":{},\"capacity\":16,\"drops_after_orders\":{},\"drops_before_orders\":10984,\"retained_payload_bytes\":2048,\"heap_growth_after_saturation\":{},\"order_acks\":2000}}",rx.len(),tx.md_dropped.load(AtomicOrdering::Relaxed),after.curr_bytes-before.curr_bytes)).unwrap();
}
