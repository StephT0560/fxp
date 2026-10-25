// SPDX-License-Identifier: MIT
use criterion::{BatchSize, Criterion, criterion_group, criterion_main};
use fxp_core::{
    engine::{CancelMsg, Clock, Engine, ExecIds, Time},
    fxp,
};
use std::hint::black_box;
struct FixedClock;
impl Clock for FixedClock {
    fn now(&self) -> Time {
        Time {
            seconds: 0,
            nanos: 0,
        }
    }
}
struct Ids(u64);
impl ExecIds for Ids {
    fn next_id(&mut self) -> String {
        self.0 += 1;
        self.0.to_string()
    }
}
fn order(i: usize) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: format!("O{i}"),
        sender: "T".into(),
        symbol: "BENCH".into(),
        price: 300_000 + (i % 100) as i64,
        quantity: 1,
        side: fxp::OrderSide::Buy as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}
fn fixture() -> Engine<(), FixedClock, Ids> {
    let mut engine = Engine::new("BENCH", FixedClock, Ids(0), None);
    for i in 0..10_000 {
        black_box(engine.new_order(order(i), ()));
    }
    engine
}
fn benchmark(c: &mut Criterion) {
    c.bench_function("engine_10k_50pct_cancel_100levels", |b| {
        b.iter_batched_ref(
            fixture,
            |engine| {
                for i in 0..5_000 {
                    black_box(engine.cancel(CancelMsg {
                        order_id: format!("O{}", i * 2),
                        sender: "T".into(),
                        client_cancel_id: String::new(),
                        output_tx: (),
                    }));
                    black_box(engine.new_order(order(10_000 + i), ()));
                }
            },
            BatchSize::LargeInput,
        )
    });
}
criterion_group!(benches, benchmark);
criterion_main!(benches);
