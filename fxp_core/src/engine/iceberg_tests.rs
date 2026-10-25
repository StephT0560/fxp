// SPDX-License-Identifier: MIT
use super::*;
struct TestClock;
impl Clock for TestClock {
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
fn engine() -> Engine<u32, TestClock, Ids> {
    Engine::new("TEST", TestClock, Ids(0), None)
}
fn order(id: &str, side: fxp::OrderSide, qty: i32, display: i32) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        sender: if side == fxp::OrderSide::Buy {
            "buyer"
        } else {
            "seller"
        }
        .into(),
        symbol: "TEST".into(),
        price: 100,
        quantity: qty,
        display_quantity: display,
        side: side as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}
fn reports(out: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    out.iter()
        .filter_map(|o| {
            if let Output::ExecutionReport(_, r) = o {
                Some(r)
            } else {
                None
            }
        })
        .collect()
}
fn replace(id: &str, new_id: &str) -> CancelReplaceMsg<u32> {
    CancelReplaceMsg {
        order_id: id.into(),
        sender: "seller".into(),
        new_order_id: new_id.into(),
        client_replace_id: new_id.into(),
        new_price: 0,
        new_quantity: 0,
        new_time_in_force: 0,
        new_stop_price: 0,
        new_display_quantity: None,
        output_tx: 1,
    }
}
#[test]
fn iceberg_reports_total_cumulative_cancel_and_replace() {
    let mut e = engine();
    let ack = e.new_order(order("I", fxp::OrderSide::Sell, 10, 3), 1);
    assert_eq!(reports(&ack)[0].display_quantity, 3);
    assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 3);
    let out = e.new_order(order("B", fxp::OrderSide::Buy, 5, 0), 2);
    let fills = reports(&out)
        .into_iter()
        .filter(|r| r.order_id == "I")
        .collect::<Vec<_>>();
    assert_eq!(
        fills
            .iter()
            .map(|r| (r.executed_quantity, r.cum_quantity, r.leaves_quantity))
            .collect::<Vec<_>>(),
        vec![(3, 3, 7), (2, 5, 5)]
    );
    assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 1);
    let mut r = replace("I", "R");
    r.new_quantity = 12;
    r.new_display_quantity = Some(2);
    let out = e.cancel_replace(r);
    let r = reports(&out)
        .into_iter()
        .find(|r| r.execution_type == fxp::ExecutionType::ExecReplaced as i32)
        .unwrap();
    assert_eq!(
        (
            r.order_quantity,
            r.cum_quantity,
            r.leaves_quantity,
            r.display_quantity
        ),
        (12, 5, 7, 2)
    );
    assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 2);
    let out = e.cancel(CancelMsg {
        order_id: "R".into(),
        sender: "seller".into(),
        client_cancel_id: "C".into(),
        output_tx: 1,
    });
    let r = reports(&out)[0];
    assert_eq!((r.cum_quantity, r.leaves_quantity), (5, 0));
    assert!(snapshot(&e.book, e.clock.now()).ask_levels.is_empty());
}
#[test]
fn iceberg_invalid_entries_and_replaces_are_atomic() {
    for tif in [
        fxp::TimeInForce::Ioc,
        fxp::TimeInForce::Fok,
        fxp::TimeInForce::AtOpen,
        fxp::TimeInForce::AtClose,
    ] {
        let mut e = engine();
        let mut o = order("I", fxp::OrderSide::Sell, 10, 3);
        o.time_in_force = tif as i32;
        assert_eq!(
            reports(&e.new_order(o, 1))[0].execution_type,
            fxp::ExecutionType::ExecRejected as i32
        );
        assert!(snapshot(&e.book, e.clock.now()).ask_levels.is_empty());
    }
    for display in [-1, 11] {
        let mut e = engine();
        assert_eq!(
            reports(&e.new_order(order("I", fxp::OrderSide::Sell, 10, display), 1))[0]
                .execution_type,
            fxp::ExecutionType::ExecRejected as i32
        );
    }
    let mut e = engine();
    e.new_order(order("I", fxp::OrderSide::Sell, 10, 3), 1);
    let mut r = replace("I", "BAD");
    r.new_display_quantity = Some(11);
    assert_eq!(
        reports(&e.cancel_replace(r))[0].execution_type,
        fxp::ExecutionType::ExecRejected as i32
    );
    assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 3);
    let mut r = replace("I", "FULL");
    r.new_display_quantity = Some(0);
    e.cancel_replace(r);
    assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 10);
}

#[test]
fn iceberg_rejects_stops_bad_lots_and_full_auction_schedule() {
    let mut e = engine();
    let mut o = order("STOP", fxp::OrderSide::Sell, 10, 2);
    o.order_type = fxp::OrderType::StopLimit as i32;
    o.stop_price = 100;
    let out = e.new_order(o, 1);
    assert!(reports(&out)[0].reject_reason.contains("icebergs require"));
    let mut e = engine().with_symbol_rules(crate::symbol_config::SymbolRules {
        lot_size: 2,
        ..Default::default()
    });
    let out = e.new_order(order("LOT", fxp::OrderSide::Sell, 10, 3), 1);
    assert!(reports(&out)[0].reject_reason.contains("lot size"));
    let mut e = Engine::new(
        "TEST",
        TestClock,
        Ids(0),
        Some(
            "open_call=00:00,open=00:01,close_call=23:58,close=23:59"
                .parse()
                .unwrap(),
        ),
    );
    // Move into continuous trading: the prohibition holds even outside the call phase.
    e.phase = fxp::TradingPhase::Continuous;
    let out = e.new_order(order("AUCTION", fxp::OrderSide::Sell, 10, 2), 1);
    assert!(reports(&out)[0].reject_reason.contains("auction"));
}

#[test]
fn iceberg_replace_withdraws_old_display_before_publishing_new_slice() {
    let mut e = engine();
    e.new_order(order("I", fxp::OrderSide::Sell, 10, 3), 1);
    let mut r = replace("I", "R");
    r.new_display_quantity = Some(2);
    r.new_price = 101;
    let out = e.cancel_replace(r);
    let changes = out
        .iter()
        .filter_map(|o| {
            if let Output::MarketData(md) = o {
                Some(&md.changes)
            } else {
                None
            }
        })
        .flatten()
        .map(|c| (c.update_type, c.price, c.quantity))
        .collect::<Vec<_>>();
    assert_eq!(changes, vec![(2, 100, 3), (0, 101, 2)]);
}

#[test]
fn iceberg_day_and_gtd_expiry_remove_only_the_visible_slice_from_depth() {
    struct C(std::cell::Cell<Time>);
    impl Clock for C {
        fn now(&self) -> Time {
            self.0.get()
        }
    }
    for tif in [fxp::TimeInForce::Day, fxp::TimeInForce::Gtd] {
        let schedule = if tif == fxp::TimeInForce::Day {
            Some("close=00:01".parse().unwrap())
        } else {
            None
        };
        let mut e = Engine::new(
            "TEST",
            C(std::cell::Cell::new(Time {
                seconds: 0,
                nanos: 0,
            })),
            Ids(0),
            schedule,
        );
        let mut o = order("I", fxp::OrderSide::Sell, 10, 2);
        o.time_in_force = tif as i32;
        o.expire_date = "1970-01-01".into();
        e.new_order(o, 1);
        e.new_order(order("B", fxp::OrderSide::Buy, 3, 0), 2);
        assert_eq!(snapshot(&e.book, e.clock.now()).ask_levels[0].size, 1);
        e.clock.0.set(Time {
            seconds: if tif == fxp::TimeInForce::Day {
                60
            } else {
                86400
            },
            nanos: 0,
        });
        let out = e.tick();
        let r = reports(&out)[0];
        assert_eq!(
            (r.execution_type, r.cum_quantity, r.leaves_quantity),
            (fxp::ExecutionType::ExecExpired as i32, 3, 0)
        );
        assert!(
            out.iter()
                .any(|o| matches!(o,Output::MarketData(md) if md.changes[0].quantity==1))
        );
        assert!(snapshot(&e.book, e.clock.now()).ask_levels.is_empty());
    }
}
#[test]
fn configured_scales_retain_int64_price_and_i128_notional_headroom() {
    for scale in [0, 2, 4, 8] {
        let mut e = engine().with_symbol_rules(crate::symbol_config::SymbolRules {
            price_scale: scale,
            ..Default::default()
        });
        let mut buy = order("B", fxp::OrderSide::Buy, i32::MAX, 0);
        buy.price = i64::MAX;
        e.new_order(buy, 1);
        let mut sell = order("S", fxp::OrderSide::Sell, i32::MAX, 0);
        sell.price = i64::MAX;
        let out = e.new_order(sell, 2);
        let fills = reports(&out)
            .into_iter()
            .filter(|r| r.execution_type == fxp::ExecutionType::ExecFilled as i32)
            .collect::<Vec<_>>();
        assert_eq!(fills.len(), 2);
        assert!(
            fills.iter().all(|r| r.avg_px == i64::MAX
                && r.cum_quantity == i32::MAX
                && r.leaves_quantity == 0)
        );
    }
}

#[test]
fn symbol_tick_rules_ignore_unused_market_and_stop_limit_prices() {
    let rules = crate::symbol_config::SymbolRules {
        tick_size: 5,
        ..Default::default()
    };
    for ty in [fxp::OrderType::Market, fxp::OrderType::Stop] {
        let mut e = engine().with_symbol_rules(rules);
        let mut o = order("UNUSED", fxp::OrderSide::Buy, 10, 0);
        o.order_type = ty as i32;
        o.price = 101;
        o.stop_price = if ty == fxp::OrderType::Stop { 105 } else { 101 };
        let out = e.new_order(o, 1);
        assert!(
            reports(&out)
                .iter()
                .all(|r| r.execution_type != fxp::ExecutionType::ExecRejected as i32),
            "{ty:?}"
        );
    }
    // A stop's trigger still must align to the tick.
    let mut e = engine().with_symbol_rules(rules);
    let mut o = order("BADSTOP", fxp::OrderSide::Buy, 10, 0);
    o.order_type = fxp::OrderType::Stop as i32;
    o.price = 101;
    o.stop_price = 101;
    assert_eq!(
        reports(&e.new_order(o, 1))[0].execution_type,
        fxp::ExecutionType::ExecRejected as i32
    );
}

#[test]
fn price_only_replace_preserves_stp_restated_quantity_below_entry_minimum() {
    for display in [0, 20] {
        let mut e = engine().with_symbol_rules(crate::symbol_config::SymbolRules {
            lot_size: 10,
            min_quantity: 20,
            ..Default::default()
        });
        e.new_order(order("B", fxp::OrderSide::Sell, 30, display), 1);
        let mut taker = order("SELF", fxp::OrderSide::Buy, 20, 0);
        taker.sender = "seller".into();
        taker.stp_mode = fxp::SelfTradePrevention::DecrementAndCancel as i32;
        let out = e.new_order(taker, 1);
        let restated = reports(&out)
            .into_iter()
            .find(|r| {
                r.order_id == "B" && r.execution_type == fxp::ExecutionType::ExecRestated as i32
            })
            .expect("STP restatement");
        assert_eq!(restated.order_quantity, 10);
        let mut r = replace("B", "R");
        r.new_price = 105;
        let out = e.cancel_replace(r);
        let report = reports(&out)
            .into_iter()
            .find(|r| r.execution_type == fxp::ExecutionType::ExecReplaced as i32)
            .expect("price-only replace accepted");
        assert_eq!(report.order_quantity, 10);
        assert_eq!(report.display_quantity, display);
        // Explicitly entered totals and floors are still validated.
        let mut r = replace("R", "BAD");
        r.new_quantity = 10;
        let out = e.cancel_replace(r);
        assert_eq!(
            reports(&out)[0].execution_type,
            fxp::ExecutionType::ExecRejected as i32
        );
        if display > 0 {
            let mut r = replace("R", "BADFLOOR");
            r.new_display_quantity = Some(display);
            let out = e.cancel_replace(r);
            assert_eq!(
                reports(&out)[0].execution_type,
                fxp::ExecutionType::ExecRejected as i32
            );
        }
    }
}
