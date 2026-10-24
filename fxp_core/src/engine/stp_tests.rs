// SPDX-License-Identifier: MIT
//! Self-trade prevention (STP) at the engine level.
use super::*;
use crate::order_book::order::StpMode;
use fxp::SelfTradePrevention as Stp;
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
fn engine() -> Engine<u32, FakeClock, Ids> {
    let clock = FakeClock(Rc::new(Cell::new(Time {
        seconds: 0,
        nanos: 0,
    })));
    Engine::new("TEST", clock, Ids(0), None)
}
#[allow(clippy::too_many_arguments)]
fn nos(
    id: &str,
    owner: &str,
    side: fxp::OrderSide,
    tif: fxp::TimeInForce,
    qty: i32,
    price: i64,
    stp: Stp,
) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        sender: owner.into(),
        symbol: "TEST".into(),
        side: side as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: tif as i32,
        price,
        quantity: qty,
        stp_mode: stp as i32,
        ..Default::default()
    }
}
fn buy(id: &str, owner: &str, qty: i32, price: i64, stp: Stp) -> fxp::NewOrderSingle {
    nos(
        id,
        owner,
        fxp::OrderSide::Buy,
        fxp::TimeInForce::Gtc,
        qty,
        price,
        stp,
    )
}
fn sell(id: &str, owner: &str, qty: i32, price: i64, stp: Stp) -> fxp::NewOrderSingle {
    nos(
        id,
        owner,
        fxp::OrderSide::Sell,
        fxp::TimeInForce::Gtc,
        qty,
        price,
        stp,
    )
}
fn reports(o: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    o.iter()
        .filter_map(|o| match o {
            Output::ExecutionReport(_, r) => Some(r),
            _ => None,
        })
        .collect()
}
fn of<'a>(o: &'a [Output<u32>], id: &str) -> Vec<&'a fxp::ExecutionReport> {
    reports(o)
        .into_iter()
        .filter(|r| r.order_id == id)
        .collect()
}
fn fills(o: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    reports(o)
        .into_iter()
        .filter(|r| r.execution_type == fxp::ExecutionType::ExecFilled as i32)
        .collect()
}
/// Market-data changes that represent a trade (ModifyOrder).
fn md_trades(o: &[Output<u32>]) -> usize {
    o.iter()
        .filter_map(|o| match o {
            Output::MarketData(md) => Some(md),
            _ => None,
        })
        .flat_map(|md| &md.changes)
        .filter(|c| c.update_type == 1)
        .count()
}
fn assert_stp_canceled(r: &fxp::ExecutionReport) {
    assert_eq!(
        r.execution_type,
        fxp::ExecutionType::ExecCanceled as i32,
        "{r:?}"
    );
    assert_eq!(r.order_status, fxp::OrderStatus::OrderCanceled as i32);
    assert_eq!(r.leaves_quantity, 0);
    assert!(
        r.reject_reason.starts_with("self-trade prevention"),
        "{r:?}"
    );
}
fn last(o: &[Output<u32>], id: &str) -> fxp::ExecutionReport {
    (*of(o, id)
        .last()
        .unwrap_or_else(|| panic!("no report for {id}")))
    .clone()
}
fn resting(e: &Engine<u32, FakeClock, Ids>, id: &str) -> Option<u32> {
    e.book.order_lookup.get(id).map(|o| o.quantity)
}

#[test]
fn stp_is_off_by_default_so_self_trades_still_print() {
    let mut e = engine();
    e.new_order(buy("B", "T", 5, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::Unspecified), 1);
    assert_eq!(fills(&out).len(), 2);
    assert_eq!(e.last_trade_price, Some(100));
}

#[test]
fn cancel_newest_cancels_the_aggressor_and_keeps_the_resting_order() {
    let mut e = engine();
    e.new_order(buy("B", "T", 5, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::CancelNewest), 1);
    assert!(fills(&out).is_empty());
    assert_eq!(md_trades(&out), 0);
    assert_stp_canceled(&last(&out, "S"));
    assert!(of(&out, "B").is_empty());
    assert_eq!(resting(&e, "B"), Some(5));
    assert_eq!(resting(&e, "S"), None);
    assert!(!e.states.contains_key("S"));
    assert_eq!(e.last_trade_price, None);
}

#[test]
fn cancel_oldest_cancels_resting_self_order_and_keeps_matching_others() {
    let mut e = engine();
    e.new_order(buy("B1", "T", 3, 100, Stp::Unspecified), 1);
    e.new_order(buy("B2", "U", 4, 100, Stp::Unspecified), 2);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::CancelOldest), 1);
    assert_stp_canceled(&last(&out, "B1"));
    let f = fills(&out);
    assert_eq!(f.len(), 2);
    assert!(f.iter().all(|r| r.executed_quantity == 4));
    assert!(f.iter().any(|r| r.order_id == "B2"));
    assert_eq!(resting(&e, "B1"), None);
    assert_eq!(resting(&e, "S"), Some(1));
    assert_eq!(md_trades(&out), 1);
}

#[test]
fn cancel_both_cancels_aggressor_and_resting_order() {
    let mut e = engine();
    e.new_order(buy("B", "T", 5, 100, Stp::Unspecified), 1);
    let out = e.new_order(
        nos(
            "S",
            "T",
            fxp::OrderSide::Sell,
            fxp::TimeInForce::Ioc,
            5,
            100,
            Stp::CancelBoth,
        ),
        1,
    );
    assert!(fills(&out).is_empty());
    assert_eq!(md_trades(&out), 0);
    assert_stp_canceled(&last(&out, "S"));
    assert_stp_canceled(&last(&out, "B"));
    assert!(e.book.order_lookup.is_empty());
    assert!(e.states.is_empty());
}

#[test]
fn decrement_and_cancel_reduces_larger_order_and_cancels_smaller() {
    // Aggressor smaller: aggressor canceled, resting restated.
    let mut e = engine();
    e.new_order(buy("B", "T", 10, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 4, 100, Stp::DecrementAndCancel), 1);
    assert!(fills(&out).is_empty());
    assert_eq!(md_trades(&out), 0);
    assert_stp_canceled(&last(&out, "S"));
    let r = last(&out, "B");
    assert_eq!(r.execution_type, fxp::ExecutionType::ExecRestated as i32);
    assert_eq!(r.order_status, fxp::OrderStatus::OrderNew as i32);
    assert_eq!(r.leaves_quantity, 6);
    assert!(r.reject_reason.starts_with("self-trade prevention"));
    assert_eq!(resting(&e, "B"), Some(6));
    let snap = snapshot(
        &e.book,
        Time {
            seconds: 0,
            nanos: 0,
        },
    );
    assert_eq!(snap.bid_levels[0].size, 6);

    // Resting smaller: resting canceled, aggressor restated and rests.
    let mut e = engine();
    e.new_order(buy("B", "T", 3, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::DecrementAndCancel), 1);
    assert!(fills(&out).is_empty());
    assert_stp_canceled(&last(&out, "B"));
    let r = last(&out, "S");
    assert_eq!(r.execution_type, fxp::ExecutionType::ExecRestated as i32);
    assert_eq!(r.leaves_quantity, 2);
    assert_eq!(resting(&e, "S"), Some(2));

    // Equal: both canceled.
    let mut e = engine();
    e.new_order(buy("B", "T", 5, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::DecrementAndCancel), 1);
    assert_stp_canceled(&last(&out, "B"));
    assert_stp_canceled(&last(&out, "S"));
    assert!(e.book.order_lookup.is_empty());
}

#[test]
fn partially_filled_resting_order_is_canceled_with_its_fill_history() {
    let mut e = engine();
    e.new_order(buy("B", "T", 10, 100, Stp::Unspecified), 1);
    e.new_order(sell("S1", "U", 4, 100, Stp::Unspecified), 2);
    let out = e.new_order(sell("S2", "T", 6, 100, Stp::CancelOldest), 1);
    let r = last(&out, "B");
    assert_stp_canceled(&r);
    assert_eq!(r.cum_quantity, 4);
    assert!(fills(&out).is_empty());
    assert_eq!(resting(&e, "S2"), Some(6));

    // Decrement against a partially filled resting order.
    let mut e = engine();
    e.new_order(buy("B", "T", 10, 100, Stp::Unspecified), 1);
    e.new_order(sell("S1", "U", 4, 100, Stp::Unspecified), 2);
    let out = e.new_order(sell("S2", "T", 2, 100, Stp::DecrementAndCancel), 1);
    let r = last(&out, "B");
    assert_eq!(r.execution_type, fxp::ExecutionType::ExecRestated as i32);
    assert_eq!(
        r.order_status,
        fxp::OrderStatus::OrderPartiallyFilled as i32
    );
    assert_eq!((r.cum_quantity, r.leaves_quantity), (4, 4));
    assert_eq!(resting(&e, "B"), Some(4));
}

#[test]
fn stp_group_scopes_matching_and_different_owners_trade() {
    let mut e = engine();
    e.new_order(buy("B", "U", 5, 100, Stp::Unspecified), 2);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::CancelNewest), 1);
    assert_eq!(fills(&out).len(), 2, "different owners are not self-trades");

    // Same owner, different groups: allowed.
    let mut e = engine();
    e.new_order(
        fxp::NewOrderSingle {
            stp_group: "T::g1".into(),
            ..buy("B", "T", 5, 100, Stp::Unspecified)
        },
        1,
    );
    let out = e.new_order(
        fxp::NewOrderSingle {
            stp_group: "T::g2".into(),
            ..sell("S", "T", 5, 100, Stp::CancelNewest)
        },
        1,
    );
    assert_eq!(fills(&out).len(), 2);

    // Same group: prevented.
    let mut e = engine();
    e.new_order(
        fxp::NewOrderSingle {
            stp_group: "T::g1".into(),
            ..buy("B", "T", 5, 100, Stp::Unspecified)
        },
        1,
    );
    let out = e.new_order(
        fxp::NewOrderSingle {
            stp_group: "T::g1".into(),
            ..sell("S", "T", 5, 100, Stp::CancelNewest)
        },
        1,
    );
    assert!(fills(&out).is_empty());
    assert_stp_canceled(&last(&out, "S"));
}

#[test]
fn ioc_remainder_canceled_by_stp_after_filling_other_owners() {
    let mut e = engine();
    e.new_order(buy("B1", "U", 2, 101, Stp::Unspecified), 2);
    e.new_order(buy("B2", "T", 3, 100, Stp::Unspecified), 1);
    let out = e.new_order(
        nos(
            "S",
            "T",
            fxp::OrderSide::Sell,
            fxp::TimeInForce::Ioc,
            5,
            100,
            Stp::CancelNewest,
        ),
        1,
    );
    assert_eq!(fills(&out).len(), 2);
    let r = last(&out, "S");
    assert_stp_canceled(&r);
    assert_eq!(r.cum_quantity, 2);
    assert_eq!(resting(&e, "B2"), Some(3));
}

#[test]
fn fok_that_would_self_match_is_canceled_without_partial_fills() {
    let mut e = engine();
    e.new_order(buy("B1", "U", 2, 101, Stp::Unspecified), 2);
    e.new_order(buy("B2", "T", 3, 100, Stp::Unspecified), 1);
    for mode in [
        Stp::CancelNewest,
        Stp::CancelOldest,
        Stp::CancelBoth,
        Stp::DecrementAndCancel,
    ] {
        let out = e.new_order(
            nos(
                "F",
                "T",
                fxp::OrderSide::Sell,
                fxp::TimeInForce::Fok,
                5,
                100,
                mode,
            ),
            1,
        );
        assert!(fills(&out).is_empty(), "{mode:?}");
        assert_eq!(md_trades(&out), 0);
        assert_stp_canceled(&last(&out, "F"));
        assert_eq!(of(&out, "B1").len() + of(&out, "B2").len(), 0);
        assert_eq!(resting(&e, "B1"), Some(2));
        assert_eq!(resting(&e, "B2"), Some(3));
    }
    // A FOK whose quantity is covered before the self order fills normally.
    let out = e.new_order(
        nos(
            "F2",
            "T",
            fxp::OrderSide::Sell,
            fxp::TimeInForce::Fok,
            2,
            100,
            Stp::CancelNewest,
        ),
        1,
    );
    assert_eq!(fills(&out).len(), 2);
}

#[test]
fn triggered_stop_and_stop_limit_apply_stp() {
    // Stop (market on trigger) with cancel-newest: canceled, resting kept.
    let mut e = engine();
    e.new_order(sell("ASK", "T", 5, 101, Stp::Unspecified), 1);
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Stop as i32,
            stop_price: 100,
            ..nos(
                "STOP",
                "T",
                fxp::OrderSide::Buy,
                fxp::TimeInForce::Day,
                5,
                0,
                Stp::CancelNewest,
            )
        },
        1,
    );
    e.new_order(sell("V", "V", 1, 100, Stp::Unspecified), 3);
    let out = e.new_order(buy("W", "W", 1, 100, Stp::Unspecified), 4);
    assert_eq!(fills(&out).len(), 2, "only the triggering print");
    assert_stp_canceled(&last(&out, "STOP"));
    assert_eq!(resting(&e, "ASK"), Some(5));
    assert!(e.stop_book.is_empty());

    // StopLimit with cancel-oldest: resting self order canceled, stop rests.
    let mut e = engine();
    e.new_order(sell("ASK", "T", 5, 101, Stp::Unspecified), 1);
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::StopLimit as i32,
            stop_price: 100,
            ..nos(
                "SL",
                "T",
                fxp::OrderSide::Buy,
                fxp::TimeInForce::Gtc,
                5,
                101,
                Stp::CancelOldest,
            )
        },
        1,
    );
    e.new_order(sell("V", "V", 1, 100, Stp::Unspecified), 3);
    let out = e.new_order(buy("W", "W", 1, 100, Stp::Unspecified), 4);
    assert_eq!(fills(&out).len(), 2);
    assert_stp_canceled(&last(&out, "ASK"));
    assert_eq!(resting(&e, "SL"), Some(5));
    assert_eq!(e.last_trade_price, Some(100));
}

#[test]
fn cancel_replace_into_a_self_cross_applies_the_replacements_stp() {
    let mut e = engine();
    e.new_order(buy("B", "T", 5, 99, Stp::Unspecified), 1);
    e.new_order(sell("S", "T", 5, 101, Stp::CancelNewest), 1);
    let out = e.cancel_replace(CancelReplaceMsg {
        order_id: "S".into(),
        sender: "T".into(),
        new_order_id: "S2".into(),
        client_replace_id: "R".into(),
        new_price: 99,
        new_quantity: 0,
        new_time_in_force: 0,
        new_stop_price: 0,
        output_tx: 1,
    });
    assert!(fills(&out).is_empty());
    assert_eq!(md_trades(&out), 0);
    assert_stp_canceled(&last(&out, "S2"));
    assert_eq!(resting(&e, "B"), Some(5));
    assert_eq!(resting(&e, "S2"), None);
}

#[test]
fn engine_default_applies_to_unspecified_and_explicit_none_opts_out() {
    let mut e = engine().with_stp_default(StpMode::CancelNewest);
    e.new_order(buy("B", "T", 5, 100, Stp::Unspecified), 1);
    let out = e.new_order(sell("S", "T", 5, 100, Stp::Unspecified), 1);
    assert_stp_canceled(&last(&out, "S"));
    let out = e.new_order(sell("S2", "T", 5, 100, Stp::None), 1);
    assert_eq!(fills(&out).len(), 2);
}

#[test]
fn unknown_stp_mode_is_rejected() {
    let mut e = engine();
    let mut o = buy("B", "T", 5, 100, Stp::Unspecified);
    o.stp_mode = 42;
    let out = e.new_order(o, 1);
    let r = last(&out, "B");
    assert_eq!(r.execution_type, fxp::ExecutionType::ExecRejected as i32);
    assert!(r.reject_reason.contains("stp_mode"));
}

#[test]
fn stp_default_parses_env_values() {
    assert_eq!(StpMode::parse(""), Ok(StpMode::None));
    assert_eq!(StpMode::parse("none"), Ok(StpMode::None));
    assert_eq!(StpMode::parse("cancel-newest"), Ok(StpMode::CancelNewest));
    assert_eq!(StpMode::parse("cancel-oldest"), Ok(StpMode::CancelOldest));
    assert_eq!(StpMode::parse("cancel-both"), Ok(StpMode::CancelBoth));
    assert_eq!(
        StpMode::parse("decrement-and-cancel"),
        Ok(StpMode::DecrementAndCancel)
    );
    assert!(StpMode::parse("bogus").is_err());
}

const SCHEDULE: &str = "open_call=00:00:10,open=00:00:20,close_call=00:00:30,close=00:00:40";

#[test]
fn auction_orders_reject_explicit_stp_and_uncross_ignores_stp() {
    let clock = FakeClock(Rc::new(Cell::new(Time {
        seconds: 11,
        nanos: 0,
    })));
    let mut e = Engine::new(
        "TEST",
        clock.clone(),
        Ids(0),
        Some(SCHEDULE.parse().unwrap()),
    )
    .with_stp_default(StpMode::CancelBoth);
    let out = e.new_order(
        nos(
            "AO",
            "T",
            fxp::OrderSide::Buy,
            fxp::TimeInForce::AtOpen,
            5,
            100,
            Stp::CancelNewest,
        ),
        1,
    );
    let r = last(&out, "AO");
    assert_eq!(r.execution_type, fxp::ExecutionType::ExecRejected as i32);
    assert!(r.reject_reason.contains("self-trade prevention"), "{r:?}");

    // With only the engine default, the AT_OPEN order is accepted and the
    // opening uncross matches it against the same owner's Day order.
    let out = e.new_order(
        nos(
            "AO",
            "T",
            fxp::OrderSide::Buy,
            fxp::TimeInForce::AtOpen,
            5,
            100,
            Stp::Unspecified,
        ),
        1,
    );
    assert_eq!(
        last(&out, "AO").execution_type,
        fxp::ExecutionType::ExecNew as i32
    );
    e.new_order(
        nos(
            "D",
            "T",
            fxp::OrderSide::Sell,
            fxp::TimeInForce::Day,
            5,
            100,
            Stp::CancelBoth,
        ),
        1,
    );
    clock.0.set(Time {
        seconds: 20,
        nanos: 0,
    });
    let out = e.tick();
    assert_eq!(fills(&out).len(), 2, "auction uncross ignores STP");
    assert!(fills(&out).iter().all(|r| r.auction));
}
