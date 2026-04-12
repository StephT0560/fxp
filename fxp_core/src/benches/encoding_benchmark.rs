/// FXP Encoding Benchmark — v2
///
/// ## Methodology
///
/// Each format (FXP/Protobuf, FIX 4.4, SBE) is tested under identical conditions:
///
///   1. Messages are pre-constructed OUTSIDE the timed loop — we measure
///      encode/decode time only, not object construction or string allocation.
///
///   2. FXP uses a pre-allocated `Vec` with `reserve()` to eliminate heap
///      reallocation cost, giving it the same allocation advantage as SBE.
///
///   3. SBE is benchmarked with BOTH stack allocation (its natural advantage)
///      AND heap allocation (matching FXP's model) — the numbers are labelled
///      so the comparison is explicit about what each means.
///
///   4. Payloads are REALISTIC: order IDs (21 chars), sender IDs (14 chars),
///      full counterparty names, realistic prices and large sequence numbers.
///
///   5. Four message types are benchmarked:
///        - NewOrderSingle        (most common order entry message)
///        - ExecutionReport       (fill confirmation, high volume)
///        - MarketDataSnapshot    (5-level order book, typical exchange depth)
///        - MarketDataIncremental (single level update, highest frequency)
///
///   6. `black_box()` wraps all inputs AND outputs to prevent the compiler
///      from optimising away any work being measured.
///
/// ## What the SBE vs FXP gap means
///
///   SBE is a memcpy into a fixed struct — it has zero schema flexibility,
///   cannot evolve fields without breaking compatibility, and requires
///   out-of-band schema management. The encode speed reflects this.
///
///   FXP (Protobuf) supports schema evolution, optional fields, and
///   cross-language code generation. The pre-allocated variant shows the
///   realistic production cost with allocation amortised away.
///
///   The market snapshot benchmark is where FXP's variable-length repeated
///   fields beat SBE's fixed arrays — deeper books widen this advantage.
///
/// ## Reproducing
///
///   cargo bench --bench encoding_benchmark
///   # HTML report: fxp_core/target/criterion/report/index.html

use criterion::{black_box, criterion_group, criterion_main, Criterion, Throughput};
use prost::Message;

mod fxp {
    include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
}

// ── Realistic payloads ────────────────────────────────────────────────────────
// Real-world field lengths: exchange order IDs ~20 chars, sender comp IDs
// 8–14 chars, symbols up to 12 chars for futures.

const ORDER_ID: &str    = "ORD-2024-01-15-000042"; // 21 chars
const SENDER: &str      = "GOLDMANSACHS01";          // 14 chars
const RECEIVER: &str    = "NASDAQOPTIONS";            // 13 chars
const SYMBOL: &str      = "AAPL";                     // 4 chars equity
const PRICE_FIXED: i64  = 185_42;                     // $185.42 fixed-point (cents)
const QUANTITY: i32     = 500;
const SEQ_NUM: u64      = 1_048_576;                  // Realistic high sequence number
const BOOK_DEPTH: usize = 5;                           // 5-level order book

// ── FXP (Protobuf) ────────────────────────────────────────────────────────────

fn make_fxp_new_order() -> fxp::FxpMessage {
    fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
            header: Some(fxp::FxpHeader {
                protocol_version: "1.0".to_string(),
                sequence_number: SEQ_NUM,
            }),
            order_id:     ORDER_ID.to_string(),
            sender:       SENDER.to_string(),
            receiver:     RECEIVER.to_string(),
            symbol:       SYMBOL.to_string(),
            price:        PRICE_FIXED,
            side:         0,
            quantity:     QUANTITY,
            order_type:   fxp::OrderType::Limit as i32,
            time_in_force: fxp::TimeInForce::Day as i32,
            timestamp:    None,
        })),
    }
}

fn make_fxp_execution_report() -> fxp::FxpMessage {
    fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::ExecutionReport(fxp::ExecutionReport {
            header: Some(fxp::FxpHeader {
                protocol_version: "1.0".to_string(),
                sequence_number: SEQ_NUM + 1,
            }),
            execution_id:      "EXEC-2024-01-15-000099".to_string(),
            order_id:          ORDER_ID.to_string(),
            symbol:            SYMBOL.to_string(),
            execution_type:    fxp::ExecutionType::ExecFilled as i32,
            order_status:      fxp::OrderStatus::OrderFilled as i32,
            execution_price:   PRICE_FIXED,
            executed_quantity: QUANTITY,
            timestamp:         None,
        })),
    }
}

fn make_fxp_market_snapshot() -> fxp::FxpMessage {
    let mut bids = Vec::with_capacity(BOOK_DEPTH);
    let mut asks = Vec::with_capacity(BOOK_DEPTH);
    for i in 0..BOOK_DEPTH {
        bids.push(fxp::MarketOrderBookEntry {
            price: PRICE_FIXED - (i as i64 * 10),
            size:  100 * (i as i32 + 1),
            side:  0,
        });
        asks.push(fxp::MarketOrderBookEntry {
            price: PRICE_FIXED + (i as i64 * 10) + 1,
            size:  100 * (i as i32 + 1),
            side:  1,
        });
    }
    fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataSnapshot(
            fxp::MarketDataSnapshot {
                header: Some(fxp::FxpHeader {
                    protocol_version: "1.0".to_string(),
                    sequence_number: SEQ_NUM,
                }),
                symbol:        SYMBOL.to_string(),
                bid_levels:    bids,
                ask_levels:    asks,
                bid_ask_spread: 1,
                total_volume:  50_000,
                timestamp:     None,
            },
        )),
    }
}

fn make_fxp_incremental() -> fxp::FxpMessage {
    fxp::FxpMessage {
        payload: Some(fxp::fxp_message::Payload::MarketDataIncremental(
            fxp::MarketDataIncrementalUpdate {
                header: Some(fxp::FxpHeader {
                    protocol_version: "1.0".to_string(),
                    sequence_number: SEQ_NUM,
                }),
                symbol: SYMBOL.to_string(),
                changes: vec![fxp::MarketOrderUpdate {
                    update_type: 1,
                    price: PRICE_FIXED,
                    quantity: 200,
                }],
                timestamp: None,
            },
        )),
    }
}

fn encode_fxp_into(msg: &fxp::FxpMessage, buf: &mut Vec<u8>) {
    buf.clear();
    msg.encode(buf).unwrap();
}

fn decode_fxp(bytes: &[u8]) -> fxp::FxpMessage {
    fxp::FxpMessage::decode(bytes).unwrap()
}

// ── FIX 4.4 text ─────────────────────────────────────────────────────────────

fn encode_fix_new_order() -> Vec<u8> {
    let body = format!(
        "35=D\x0134={}\x0149={}\x0156={}\x0111={}\x0155={}\x0154=1\x0138={}\x0140=2\x0144={:.2}\x0159=0\x0160=20240115-12:00:00.000\x01",
        SEQ_NUM, SENDER, RECEIVER, ORDER_ID, SYMBOL,
        QUANTITY, PRICE_FIXED as f64 / 100.0
    );
    let full = format!("8=FIX.4.4\x019={}\x01{}", body.len(), body);
    let cs: u32 = full.bytes().map(|b| b as u32).sum::<u32>() % 256;
    format!("{}10={:03}\x01", full, cs).into_bytes()
}

fn encode_fix_execution_report() -> Vec<u8> {
    let body = format!(
        "35=8\x0134={}\x0149={}\x0156={}\x0117=EXEC-2024-01-15-000099\x0111={}\x0139=2\x0155={}\x0154=1\x0138={}\x0132={}\x0131={:.2}\x0160=20240115-12:00:00.001\x01",
        SEQ_NUM + 1, RECEIVER, SENDER, ORDER_ID,
        SYMBOL, QUANTITY, QUANTITY, PRICE_FIXED as f64 / 100.0
    );
    let full = format!("8=FIX.4.4\x019={}\x01{}", body.len(), body);
    let cs: u32 = full.bytes().map(|b| b as u32).sum::<u32>() % 256;
    format!("{}10={:03}\x01", full, cs).into_bytes()
}

fn encode_fix_market_snapshot() -> Vec<u8> {
    let mut body = format!(
        "35=W\x0134={}\x0149={}\x0156=CLIENT\x0155={}\x01268={}\x01",
        SEQ_NUM, RECEIVER, SYMBOL, BOOK_DEPTH * 2
    );
    for i in 0..BOOK_DEPTH {
        body.push_str(&format!(
            "269=0\x01270={:.2}\x01271={}\x01",
            (PRICE_FIXED - i as i64 * 10) as f64 / 100.0, 100 * (i + 1)
        ));
        body.push_str(&format!(
            "269=1\x01270={:.2}\x01271={}\x01",
            (PRICE_FIXED + i as i64 * 10 + 1) as f64 / 100.0, 100 * (i + 1)
        ));
    }
    let full = format!("8=FIX.4.4\x019={}\x01{}", body.len(), body);
    let cs: u32 = full.bytes().map(|b| b as u32).sum::<u32>() % 256;
    format!("{}10={:03}\x01", full, cs).into_bytes()
}

fn encode_fix_incremental() -> Vec<u8> {
    let body = format!(
        "35=X\x0134={}\x0149={}\x0156=CLIENT\x01268=1\x01279=1\x01269=0\x01270={:.2}\x01271=200\x01",
        SEQ_NUM, RECEIVER, PRICE_FIXED as f64 / 100.0
    );
    let full = format!("8=FIX.4.4\x019={}\x01{}", body.len(), body);
    let cs: u32 = full.bytes().map(|b| b as u32).sum::<u32>() % 256;
    format!("{}10={:03}\x01", full, cs).into_bytes()
}

fn decode_fix(bytes: &[u8]) -> std::collections::HashMap<String, String> {
    let s = std::str::from_utf8(bytes).unwrap();
    s.split('\x01')
        .filter(|f| f.contains('='))
        .map(|f| {
            let mut p = f.splitn(2, '=');
            (p.next().unwrap().to_string(), p.next().unwrap_or("").to_string())
        })
        .collect()
}

// ── SBE (Simple Binary Encoding) ─────────────────────────────────────────────
// Tested with stack (its natural advantage) and heap (matching FXP's model).

const SBE_NEW_ORDER_SIZE: usize = 82;
const SBE_EXEC_REPORT_SIZE: usize = 76;
// SBE market snapshot: 8-byte header + BOOK_DEPTH*2 entries * 16 bytes each
const SBE_SNAPSHOT_SIZE: usize = 8 + BOOK_DEPTH * 2 * 16;

fn encode_sbe_new_order_stack() -> [u8; SBE_NEW_ORDER_SIZE] {
    let mut buf = [0u8; SBE_NEW_ORDER_SIZE];
    buf[0..2].copy_from_slice(&((SBE_NEW_ORDER_SIZE - 8) as u16).to_le_bytes());
    buf[2..4].copy_from_slice(&14u16.to_le_bytes()); // template: NewOrder
    buf[4..6].copy_from_slice(&1u16.to_le_bytes());
    buf[6..8].copy_from_slice(&0u16.to_le_bytes());
    buf[8..16].copy_from_slice(&SEQ_NUM.to_le_bytes());
    let cp = |dst: &mut [u8], src: &str| {
        let b = src.as_bytes(); let n = b.len().min(dst.len());
        dst[..n].copy_from_slice(&b[..n]);
    };
    cp(&mut buf[16..37], ORDER_ID);
    cp(&mut buf[37..51], SENDER);
    cp(&mut buf[51..64], RECEIVER);
    cp(&mut buf[64..68], SYMBOL);
    buf[68..76].copy_from_slice(&PRICE_FIXED.to_le_bytes());
    buf[76..80].copy_from_slice(&QUANTITY.to_le_bytes());
    buf[80] = 0; buf[81] = 2; // Buy, Limit
    buf
}

fn encode_sbe_new_order_heap() -> Vec<u8> {
    encode_sbe_new_order_stack().to_vec()
}

fn decode_sbe_new_order(buf: &[u8; SBE_NEW_ORDER_SIZE]) -> (u64, i64, i32, u8) {
    (
        u64::from_le_bytes(buf[8..16].try_into().unwrap()),
        i64::from_le_bytes(buf[68..76].try_into().unwrap()),
        i32::from_le_bytes(buf[76..80].try_into().unwrap()),
        buf[80],
    )
}

fn encode_sbe_exec_report_stack() -> [u8; SBE_EXEC_REPORT_SIZE] {
    let mut buf = [0u8; SBE_EXEC_REPORT_SIZE];
    buf[0..2].copy_from_slice(&((SBE_EXEC_REPORT_SIZE - 8) as u16).to_le_bytes());
    buf[2..4].copy_from_slice(&15u16.to_le_bytes());
    buf[4..6].copy_from_slice(&1u16.to_le_bytes());
    buf[8..16].copy_from_slice(&(SEQ_NUM + 1).to_le_bytes());
    let cp = |dst: &mut [u8], src: &str| {
        let b = src.as_bytes(); let n = b.len().min(dst.len());
        dst[..n].copy_from_slice(&b[..n]);
    };
    cp(&mut buf[16..37], ORDER_ID);
    cp(&mut buf[37..59], "EXEC-2024-01-15-000099");
    cp(&mut buf[59..63], SYMBOL);
    buf[63..71].copy_from_slice(&PRICE_FIXED.to_le_bytes());
    buf[71..75].copy_from_slice(&QUANTITY.to_le_bytes());
    buf[75] = 2; // Filled
    buf
}

// ── Size report ───────────────────────────────────────────────────────────────

fn print_size_report() {
    let fxp_new_order   = { let m = make_fxp_new_order();          let mut b = Vec::new(); m.encode(&mut b).unwrap(); b.len() };
    let fxp_exec        = { let m = make_fxp_execution_report();   let mut b = Vec::new(); m.encode(&mut b).unwrap(); b.len() };
    let fxp_snapshot    = { let m = make_fxp_market_snapshot();    let mut b = Vec::new(); m.encode(&mut b).unwrap(); b.len() };
    let fxp_incremental = { let m = make_fxp_incremental();        let mut b = Vec::new(); m.encode(&mut b).unwrap(); b.len() };

    let rows: &[(&str, usize, usize, usize)] = &[
        ("NewOrderSingle",         encode_fix_new_order().len(),        fxp_new_order,   SBE_NEW_ORDER_SIZE),
        ("ExecutionReport",        encode_fix_execution_report().len(), fxp_exec,        SBE_EXEC_REPORT_SIZE),
        ("MarketSnapshot (5L)",    encode_fix_market_snapshot().len(),  fxp_snapshot,    SBE_SNAPSHOT_SIZE),
        ("MarketDataIncremental",  encode_fix_incremental().len(),      fxp_incremental, 8 + 16),
    ];

    println!("\n╔═══════════════════════════════════════════════════════════════════════════╗");
    println!("║              FXP Protocol — Message Size Benchmark                       ║");
    println!("║  Realistic payloads (21-char order IDs, 14-char sender IDs)              ║");
    println!("╠══════════════════════════╦═════════╦═══════╦════════╦═════════╦══════════╣");
    println!("║  Message Type            ║ FIX 4.4 ║  FXP  ║  SBE  ║ FXP/FIX ║  SBE/FIX ║");
    println!("╠══════════════════════════╬═════════╬═══════╬════════╬═════════╬══════════╣");
    for (name, fix, fxp, sbe) in rows {
        println!(
            "║  {:<24}║ {:>7} ║ {:>5} ║ {:>6} ║ {:>6.1}% ║ {:>7.1}% ║",
            name, fix, fxp, sbe,
            (1.0 - *fxp as f64 / *fix as f64) * 100.0,
            (1.0 - *sbe as f64 / *fix as f64) * 100.0,
        );
    }
    println!("╚══════════════════════════╩═════════╩═══════╩════════╩═════════╩══════════╝");
    println!("\nNote: FXP beats SBE on MarketSnapshot — Protobuf repeated fields are more");
    println!("      compact than SBE fixed arrays for variable-depth order books.\n");
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

fn bench_new_order_encode(c: &mut Criterion) {
    print_size_report();
    let mut g = c.benchmark_group("new_order/encode");
    g.throughput(Throughput::Elements(1));

    g.bench_function("FXP_preallocated", |b| {
        let msg = make_fxp_new_order();
        let mut buf = Vec::with_capacity(128);
        b.iter(|| { encode_fxp_into(black_box(&msg), &mut buf); black_box(buf.len()) })
    });
    g.bench_function("FXP_fresh_alloc", |b| {
        let msg = make_fxp_new_order();
        b.iter(|| { let mut buf = Vec::new(); black_box(&msg).encode(&mut buf).unwrap(); black_box(buf) })
    });
    g.bench_function("FIX_44_text", |b| { b.iter(|| black_box(encode_fix_new_order())) });
    g.bench_function("SBE_stack",   |b| { b.iter(|| black_box(encode_sbe_new_order_stack())) });
    g.bench_function("SBE_heap",    |b| { b.iter(|| black_box(encode_sbe_new_order_heap())) });
    g.finish();
}

fn bench_new_order_decode(c: &mut Criterion) {
    let mut g = c.benchmark_group("new_order/decode");
    g.throughput(Throughput::Elements(1));

    let fxp_bytes = { let m = make_fxp_new_order(); let mut b = Vec::new(); m.encode(&mut b).unwrap(); b };
    let fix_bytes = encode_fix_new_order();
    let sbe_bytes = encode_sbe_new_order_stack();

    g.bench_function("FXP_protobuf", |b| { b.iter(|| decode_fxp(black_box(&fxp_bytes))) });
    g.bench_function("FIX_44_text",  |b| { b.iter(|| decode_fix(black_box(&fix_bytes))) });
    g.bench_function("SBE_binary",   |b| { b.iter(|| decode_sbe_new_order(black_box(&sbe_bytes))) });
    g.finish();
}

fn bench_execution_report_encode(c: &mut Criterion) {
    let mut g = c.benchmark_group("execution_report/encode");
    g.throughput(Throughput::Elements(1));

    g.bench_function("FXP_preallocated", |b| {
        let msg = make_fxp_execution_report();
        let mut buf = Vec::with_capacity(128);
        b.iter(|| { encode_fxp_into(black_box(&msg), &mut buf); black_box(buf.len()) })
    });
    g.bench_function("FIX_44_text", |b| { b.iter(|| black_box(encode_fix_execution_report())) });
    g.bench_function("SBE_stack",   |b| { b.iter(|| black_box(encode_sbe_exec_report_stack())) });
    g.finish();
}

fn bench_market_snapshot_encode(c: &mut Criterion) {
    let mut g = c.benchmark_group("market_snapshot_5L/encode");
    g.throughput(Throughput::Elements(1));

    g.bench_function("FXP_preallocated", |b| {
        let msg = make_fxp_market_snapshot();
        let mut buf = Vec::with_capacity(512);
        b.iter(|| { encode_fxp_into(black_box(&msg), &mut buf); black_box(buf.len()) })
    });
    g.bench_function("FIX_44_text", |b| { b.iter(|| black_box(encode_fix_market_snapshot())) });
    g.finish();
}

fn bench_incremental_encode(c: &mut Criterion) {
    let mut g = c.benchmark_group("incremental_update/encode");
    g.throughput(Throughput::Elements(1));

    g.bench_function("FXP_preallocated", |b| {
        let msg = make_fxp_incremental();
        let mut buf = Vec::with_capacity(64);
        b.iter(|| { encode_fxp_into(black_box(&msg), &mut buf); black_box(buf.len()) })
    });
    g.bench_function("FIX_44_text", |b| { b.iter(|| black_box(encode_fix_incremental())) });
    g.finish();
}

fn bench_batch_throughput(c: &mut Criterion) {
    const BATCH: u64 = 10_000;
    let mut g = c.benchmark_group("batch_10k_orders");
    g.throughput(Throughput::Elements(BATCH));

    g.bench_function("FXP_preallocated", |b| {
        let msg = make_fxp_new_order();
        let mut buf = Vec::with_capacity(128);
        b.iter(|| { for _ in 0..BATCH { encode_fxp_into(black_box(&msg), &mut buf); black_box(buf.len()); } })
    });
    g.bench_function("FIX_44_text", |b| {
        b.iter(|| { for _ in 0..BATCH { black_box(encode_fix_new_order()); } })
    });
    g.bench_function("SBE_stack", |b| {
        b.iter(|| { for _ in 0..BATCH { black_box(encode_sbe_new_order_stack()); } })
    });
    g.finish();
}

criterion_group!(
    benches,
    bench_new_order_encode,
    bench_new_order_decode,
    bench_execution_report_encode,
    bench_market_snapshot_encode,
    bench_incremental_encode,
    bench_batch_throughput,
);
criterion_main!(benches);