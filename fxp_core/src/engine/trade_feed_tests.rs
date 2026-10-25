// SPDX-License-Identifier: MIT
use super::*;
use std::cell::Cell;
use std::rc::Rc;

#[derive(Clone)]
struct FakeClock(Rc<Cell<Time>>);

impl Clock for FakeClock {
    fn now(&self) -> Time {
        self.0.get()
    }
}

struct Ids(u64);

impl ExecIds for Ids {
    fn next_id(&mut self) -> String {
        self.0 += 1;
        format!("X{}", self.0)
    }
}

fn clock(seconds: i64) -> FakeClock {
    FakeClock(Rc::new(Cell::new(Time { seconds, nanos: 0 })))
}

fn engine() -> Engine<u32, FakeClock, Ids> {
    Engine::new("TEST", clock(0), Ids(0), None)
}

fn order(id: &str, side: fxp::OrderSide, qty: i32, price: i64) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        sender: "owner".into(),
        symbol: "TEST".into(),
        side: side as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        price,
        quantity: qty,
        account: format!("acct-{id}"),
        client_order_id: format!("client-{id}"),
        ..Default::default()
    }
}

fn trades(outputs: &[Output<u32>]) -> Vec<&fxp::TradeCaptureReport> {
    outputs
        .iter()
        .filter_map(|output| match output {
            Output::Trade(trade) => Some(trade),
            _ => None,
        })
        .collect()
}

fn filled_reports(outputs: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    outputs
        .iter()
        .filter_map(|output| match output {
            Output::ExecutionReport(_, report)
                if report.execution_type == fxp::ExecutionType::ExecFilled as i32 =>
            {
                Some(report)
            }
            _ => None,
        })
        .collect()
}

#[test]
fn single_fill_emits_anonymous_public_trade() {
    let mut engine = engine();
    engine.new_order(order("ASK", fxp::OrderSide::Sell, 3, 100), 1);
    let output = engine.new_order(order("BID", fxp::OrderSide::Buy, 3, 105), 2);
    let found = trades(&output);
    let [trade] = found.as_slice() else {
        panic!("expected one public trade");
    };
    assert_eq!(
        (
            trade.symbol.as_str(),
            trade.trade_id.as_str(),
            trade.seq,
            trade.trade_price,
            trade.trade_quantity,
            trade.aggressor_side,
            trade.is_auction,
            trade.buy_order_id.as_str(),
            trade.sell_order_id.as_str(),
        ),
        (
            "TEST",
            "TEST:1",
            1,
            100,
            3,
            fxp::OrderSide::Buy as i32,
            false,
            "",
            "",
        )
    );
    assert_eq!(
        trade.header.as_ref().unwrap().protocol_version,
        crate::PROTOCOL_VERSION
    );
    assert_eq!(trade.header.as_ref().unwrap().sequence_number, 0);
}

#[test]
fn multi_level_sweep_emits_one_ordered_print_per_fill() {
    let mut engine = engine();
    engine.new_order(order("ASK1", fxp::OrderSide::Sell, 1, 100), 1);
    engine.new_order(order("ASK2", fxp::OrderSide::Sell, 2, 101), 1);
    let output = engine.new_order(order("BID", fxp::OrderSide::Buy, 3, 102), 2);
    assert_eq!(
        trades(&output)
            .iter()
            .map(|trade| (trade.seq, trade.trade_price, trade.trade_quantity))
            .collect::<Vec<_>>(),
        [(1, 100, 1), (2, 101, 2)]
    );
}

#[test]
fn iceberg_prints_only_executed_display_slices() {
    let mut engine = engine();
    let mut iceberg = order("ICE", fxp::OrderSide::Sell, 10, 100);
    iceberg.display_quantity = 3;
    engine.new_order(iceberg, 1);
    let output = engine.new_order(order("BID", fxp::OrderSide::Buy, 5, 100), 2);
    let printed = trades(&output);
    assert_eq!(
        printed
            .iter()
            .map(|trade| (trade.seq, trade.trade_quantity))
            .collect::<Vec<_>>(),
        [(1, 3), (2, 2)]
    );
    assert_eq!(
        printed
            .iter()
            .map(|trade| trade.trade_quantity)
            .sum::<i32>(),
        5
    );
}

#[test]
fn stop_cascade_prints_follow_fill_execution_report_order() {
    let mut engine = engine();
    engine.new_order(order("ASK1", fxp::OrderSide::Sell, 1, 100), 1);
    engine.new_order(order("ASK2", fxp::OrderSide::Sell, 1, 101), 1);
    let mut stop = order("STOP", fxp::OrderSide::Buy, 1, 0);
    stop.order_type = fxp::OrderType::Stop as i32;
    stop.stop_price = 100;
    engine.new_order(stop, 3);

    let output = engine.new_order(order("BID", fxp::OrderSide::Buy, 1, 100), 2);
    assert_eq!(
        trades(&output)
            .iter()
            .map(|trade| (trade.seq, trade.trade_price, trade.aggressor_side))
            .collect::<Vec<_>>(),
        [
            (1, 100, fxp::OrderSide::Buy as i32),
            (2, 101, fxp::OrderSide::Buy as i32)
        ]
    );
    let filled_ids = filled_reports(&output)
        .iter()
        .map(|report| report.order_id.as_str())
        .collect::<Vec<_>>();
    assert_eq!(filled_ids, ["BID", "ASK1", "STOP", "ASK2"]);
    let trade_positions = output
        .iter()
        .filter_map(|output| match output {
            Output::Trade(trade) => Some(trade.seq),
            _ => None,
        })
        .collect::<Vec<_>>();
    assert_eq!(trade_positions, [1, 2]);
}

#[test]
fn auction_uncross_emits_one_auction_print_per_matched_pair() {
    let clock = clock(11);
    let schedule = Some(
        "open_call=00:00:10,open=00:00:20,close_call=00:00:40,close=00:00:50"
            .parse()
            .unwrap(),
    );
    let mut engine = Engine::new("TEST", clock.clone(), Ids(0), schedule);
    engine.new_order(order("BID", fxp::OrderSide::Buy, 2, 101), 1);
    engine.new_order(order("ASK", fxp::OrderSide::Sell, 2, 100), 2);
    clock.0.set(Time {
        seconds: 20,
        nanos: 0,
    });
    let output = engine.tick();
    let found = trades(&output);
    let [trade] = found.as_slice() else {
        panic!("expected one auction print");
    };
    assert_eq!(
        (
            trade.seq,
            trade.trade_price,
            trade.trade_quantity,
            trade.aggressor_side,
            trade.is_auction,
            trade.buy_order_id.as_str(),
            trade.sell_order_id.as_str(),
        ),
        (1, 100, 2, fxp::OrderSide::Unspecified as i32, true, "", "",)
    );
}

#[test]
fn prevented_canceled_and_expired_orders_do_not_consume_trade_sequences() {
    for mode in [
        fxp::SelfTradePrevention::CancelNewest,
        fxp::SelfTradePrevention::DecrementAndCancel,
    ] {
        let mut engine = engine();
        let mut resting = order("BID", fxp::OrderSide::Buy, 3, 100);
        resting.sender = "same-owner".into();
        engine.new_order(resting, 1);
        let mut incoming = order("ASK", fxp::OrderSide::Sell, 3, 100);
        incoming.sender = "same-owner".into();
        incoming.stp_mode = mode as i32;
        let output = engine.new_order(incoming, 1);
        assert!(trades(&output).is_empty());
        assert_eq!(engine.trade_seq, 0);
    }

    let mut engine = engine();
    engine.new_order(order("CANCEL", fxp::OrderSide::Sell, 2, 110), 1);
    let cancel = engine.cancel(CancelMsg {
        order_id: "CANCEL".into(),
        sender: "owner".into(),
        client_cancel_id: "cancel".into(),
        output_tx: 1,
    });
    assert!(trades(&cancel).is_empty());
    assert_eq!(engine.trade_seq, 0);

    let day_clock = clock(0);
    let schedule = Some("close=00:01".parse().unwrap());
    let mut engine = Engine::new("TEST", day_clock.clone(), Ids(0), schedule);
    engine.new_order(
        fxp::NewOrderSingle {
            time_in_force: fxp::TimeInForce::Day as i32,
            ..order("DAY", fxp::OrderSide::Sell, 1, 110)
        },
        1,
    );
    day_clock.0.set(Time {
        seconds: 60,
        nanos: 0,
    });
    assert!(trades(&engine.tick()).is_empty());
    assert_eq!(engine.trade_seq, 0);

    let clock = clock(0);
    let mut engine = Engine::new("TEST", clock.clone(), Ids(0), None);
    engine.new_order(
        fxp::NewOrderSingle {
            time_in_force: fxp::TimeInForce::Gtd as i32,
            expire_date: "1970-01-01".into(),
            ..order("GTD", fxp::OrderSide::Sell, 1, 110)
        },
        1,
    );
    clock.0.set(Time {
        seconds: 86_400,
        nanos: 0,
    });
    assert!(trades(&engine.tick()).is_empty());
    assert_eq!(engine.trade_seq, 0);
}

#[test]
fn seeded_engine_fills_match_trade_quantity_and_sequence_invariants() {
    struct Rng(u64);
    impl Rng {
        fn next(&mut self) -> u64 {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            self.0
        }
        fn range(&mut self, max: u64) -> u64 {
            self.next() % max
        }
    }

    for seed in 1..=300 {
        let mut rng = Rng(seed);
        let mut engine = engine();
        let mut all_trades = Vec::new();
        let mut executed_quantity = 0_u64;
        let mut live_ids: Vec<String> = Vec::new();
        let mut next_id = 0_u64;

        for _ in 0..80 {
            let kind = rng.range(9);
            let output = if kind == 6 && !live_ids.is_empty() {
                let id = live_ids[rng.range(live_ids.len() as u64) as usize].clone();
                engine.cancel(CancelMsg {
                    order_id: id,
                    sender: "owner".into(),
                    client_cancel_id: format!("C-{seed}-{next_id}"),
                    output_tx: 1,
                })
            } else if kind == 7 && !live_ids.is_empty() {
                let id = live_ids[rng.range(live_ids.len() as u64) as usize].clone();
                next_id += 1;
                engine.cancel_replace(CancelReplaceMsg {
                    order_id: id,
                    sender: "owner".into(),
                    new_order_id: format!("R-{seed}-{next_id}"),
                    client_replace_id: format!("CR-{seed}-{next_id}"),
                    new_price: 95 + rng.range(11) as i64,
                    new_quantity: 1 + rng.range(5) as i32,
                    new_time_in_force: fxp::TimeInForce::Unspecified as i32,
                    new_stop_price: 0,
                    new_display_quantity: None,
                    output_tx: 1,
                })
            } else {
                next_id += 1;
                let id = format!("O-{seed}-{next_id}");
                let side = if rng.range(2) == 0 {
                    fxp::OrderSide::Buy
                } else {
                    fxp::OrderSide::Sell
                };
                let mut order = order(
                    &id,
                    side,
                    1 + rng.range(5) as i32,
                    95 + rng.range(11) as i64,
                );
                match kind {
                    1 => {
                        order.order_type = fxp::OrderType::Market as i32;
                        order.price = 0;
                        order.time_in_force = fxp::TimeInForce::Ioc as i32;
                    }
                    2 => order.time_in_force = fxp::TimeInForce::Ioc as i32,
                    3 => order.time_in_force = fxp::TimeInForce::Fok as i32,
                    4 => {
                        order.order_type = if rng.range(2) == 0 {
                            fxp::OrderType::Stop
                        } else {
                            fxp::OrderType::StopLimit
                        } as i32;
                        order.stop_price = 95 + rng.range(11) as i64;
                    }
                    5 => {
                        order.display_quantity = 1 + rng.range(order.quantity as u64) as i32;
                    }
                    8 => {
                        order.stp_mode = fxp::SelfTradePrevention::CancelNewest as i32;
                    }
                    _ => {}
                }
                engine.new_order(order, 1)
            };

            for output in output {
                match output {
                    Output::Trade(trade) => all_trades.push(trade),
                    Output::ExecutionReport(_, report)
                        if report.execution_type == fxp::ExecutionType::ExecFilled as i32 =>
                    {
                        executed_quantity += report.executed_quantity as u64;
                    }
                    _ => {}
                }
            }
            live_ids.extend(engine.states.keys().cloned());
            live_ids.sort();
            live_ids.dedup();
        }

        assert_eq!(
            executed_quantity,
            all_trades
                .iter()
                .map(|trade| trade.trade_quantity as u64)
                .sum::<u64>()
                * 2,
            "fill totals differ for seed {seed}"
        );
        assert!(all_trades.iter().all(|trade| trade.trade_quantity > 0));
        assert_eq!(
            all_trades.iter().map(|trade| trade.seq).collect::<Vec<_>>(),
            (1..=all_trades.len() as u64).collect::<Vec<_>>(),
            "trade sequence differs for seed {seed}"
        );
    }
}
