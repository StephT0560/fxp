// SPDX-License-Identifier: MIT
use super::*;

#[test]
fn queued_market_cancel_replace_ownership_and_duplicate_ids() {
    let (mut e, clock) = setup(11);
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Market as i32,
            ..buy("M", fxp::TimeInForce::Gtc, 5, 0)
        },
        7,
    );
    rejected(
        &e.cancel_replace(CancelReplaceMsg {
            sender: "other".into(),
            ..replace("M", "M2", 0, fxp::TimeInForce::Unspecified)
        }),
        "Unknown order",
    );
    rejected(
        &e.new_order(buy("M", fxp::TimeInForce::Gtc, 1, 100), 7),
        "Duplicate",
    );
    let replaced = e.cancel_replace(CancelReplaceMsg {
        new_quantity: 8,
        ..replace("M", "M2", 0, fxp::TimeInForce::Unspecified)
    });
    assert_eq!(reports(&replaced).len(), 2);
    assert_eq!(infos(&replaced)[0].imbalance_quantity, 8);
    assert!(!e.states.contains_key("M"));
    let canceled = e.cancel(cancel("M2"));
    assert_eq!(
        reports(&canceled)[0].execution_type,
        fxp::ExecutionType::ExecCanceled as i32
    );
    assert_eq!(infos(&canceled)[0].imbalance_quantity, 0);
    advance(&clock, 20);
    assert!(fills(&e.tick()).is_empty());
    assert!(e.states.is_empty());
}

#[test]
fn continuous_replace_can_hold_a_limit_for_close_and_call_replace_can_repark_a_market() {
    let (mut e, clock) = setup(21);
    e.new_order(buy("B", fxp::TimeInForce::Gtc, 4, 100), 7);
    let held = e.cancel_replace(replace("B", "H", 100, fxp::TimeInForce::AtClose));
    assert_eq!(reports(&held).len(), 2);
    assert!(!e.book.order_lookup.contains_key("H"));
    assert!(e.held_close.contains_key("H"));
    advance(&clock, 30);
    e.tick();
    assert!(e.book.order_lookup.contains_key("H"));
    let (mut e, _) = setup(11);
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Market as i32,
            ..buy("M", fxp::TimeInForce::Gtc, 5, 0)
        },
        7,
    );
    let held = e.cancel_replace(replace("M", "H", 0, fxp::TimeInForce::AtClose));
    assert!(fills(&held).is_empty());
    assert_eq!(infos(&held)[0].imbalance_quantity, 0);
    assert!(e.call_markets.is_empty());
    assert!(e.held_close.contains_key("H"));
}

#[test]
fn at_close_markets_held_during_opening_join_the_closing_call() {
    let (mut e, clock) = setup(11);
    let held = e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Market as i32,
            ..buy("M", fxp::TimeInForce::AtClose, 5, 0)
        },
        7,
    );
    assert!(infos(&held).is_empty());
    assert!(!held.iter().any(|o| matches!(o, Output::MarketData(_))));
    e.new_order(sell("S", fxp::TimeInForce::Gtc, 2, 100), 8);
    advance(&clock, 30);
    e.tick();
    assert_eq!(e.call_markets["M"].quantity, 5);
    advance(&clock, 40);
    let out = e.tick();
    let f = fills(&out);
    assert_eq!(f.len(), 2);
    assert!(f.iter().all(|r| r.auction && r.execution_price == 100));
    assert!(reports(&out).iter().any(|r| r.order_id == "M"
        && r.cum_quantity == 2
        && r.execution_type == fxp::ExecutionType::ExecCanceled as i32));
}

#[test]
fn market_only_opening_auction_uses_reference_and_owners_receive_their_reports() {
    for reference in [None, Some(123)] {
        let (mut e, clock) = setup(11);
        e.last_trade_price = reference;
        for (id, side, owner) in [
            ("B", fxp::OrderSide::Buy, 7),
            ("S", fxp::OrderSide::Sell, 8),
        ] {
            e.new_order(
                fxp::NewOrderSingle {
                    order_type: fxp::OrderType::Market as i32,
                    ..order(id, side, fxp::TimeInForce::Gtc, 3, 0)
                },
                owner,
            );
        }
        advance(&clock, 20);
        let out = e.tick();
        assert_eq!(fills(&out).len(), if reference.is_some() { 2 } else { 0 });
        for output in &out {
            if let Output::ExecutionReport(owner, report) = output {
                assert_eq!(*owner, if report.order_id == "B" { 7 } else { 8 });
                if report.auction {
                    assert_eq!(
                        (
                            report.execution_price,
                            report.executed_quantity,
                            report.avg_px
                        ),
                        (123, 3, 123)
                    );
                    assert_eq!(report.account, "acct");
                }
            }
        }
        assert!(e.states.is_empty());
        assert!(e.call_markets.is_empty());
    }
}

#[test]
fn gtd_expiry_at_open_call_and_day_expiry_remove_parked_stops_without_matching() {
    let schedule: SessionSchedule =
        "open_call=00:00:00,open=00:00:10,close_call=00:00:20,close=23:59:59"
            .parse()
            .unwrap();
    let clock = FakeClock(Rc::new(Cell::new(Time {
        seconds: 86398,
        nanos: 0,
    })));
    let mut e = Engine::new("TEST", clock.clone(), Ids(0), Some(schedule));
    e.new_order(
        fxp::NewOrderSingle {
            expire_date: "1970-01-01".into(),
            ..buy("G", fxp::TimeInForce::Gtd, 3, 100)
        },
        7,
    );
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Stop as i32,
            stop_price: 100,
            ..buy("STOP", fxp::TimeInForce::Day, 2, 0)
        },
        7,
    );
    advance(&clock, 86400);
    let out = e.tick();
    assert!(fills(&out).is_empty());
    let expired: Vec<_> = reports(&out)
        .into_iter()
        .filter(|r| r.execution_type == fxp::ExecutionType::ExecExpired as i32)
        .map(|r| r.order_id.as_str())
        .collect();
    assert_eq!(expired, ["STOP", "G"]);
    assert!(e.states.is_empty());
    assert!(e.stop_book.is_empty());
    assert!(e.book.order_lookup.is_empty());
    assert_eq!(e.phase(), fxp::TradingPhase::OpeningCall);
    assert_eq!(infos(&out)[0].indicative_volume, 0);
}

use std::cell::Cell;
use std::rc::Rc;

const SCHEDULE: &str = "open_call=00:00:10,open=00:00:20,close_call=00:00:30,close=00:00:40";

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
fn setup(at: i64) -> (Engine<u32, FakeClock, Ids>, FakeClock) {
    let clock = FakeClock(Rc::new(Cell::new(Time {
        seconds: at,
        nanos: 0,
    })));
    (
        Engine::new(
            "TEST",
            clock.clone(),
            Ids(0),
            Some(SCHEDULE.parse().unwrap()),
        ),
        clock,
    )
}
fn advance(clock: &FakeClock, at: i64) {
    clock.0.set(Time {
        seconds: at,
        nanos: 0,
    });
}
fn order(
    id: &str,
    side: fxp::OrderSide,
    tif: fxp::TimeInForce,
    qty: i32,
    price: i64,
) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        symbol: "TEST".into(),
        sender: "owner".into(),
        side: side as i32,
        time_in_force: tif as i32,
        quantity: qty,
        price,
        order_type: fxp::OrderType::Limit as i32,
        account: "acct".into(),
        client_order_id: id.into(),
        ..Default::default()
    }
}
fn buy(id: &str, tif: fxp::TimeInForce, qty: i32, price: i64) -> fxp::NewOrderSingle {
    order(id, fxp::OrderSide::Buy, tif, qty, price)
}
fn sell(id: &str, tif: fxp::TimeInForce, qty: i32, price: i64) -> fxp::NewOrderSingle {
    order(id, fxp::OrderSide::Sell, tif, qty, price)
}
fn reports(o: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    o.iter()
        .filter_map(|o| match o {
            Output::ExecutionReport(_, r) => Some(r),
            _ => None,
        })
        .collect()
}
fn infos(o: &[Output<u32>]) -> Vec<&fxp::AuctionInfo> {
    o.iter()
        .filter_map(|o| match o {
            Output::AuctionInfo(i) => Some(i),
            _ => None,
        })
        .collect()
}
fn fills(o: &[Output<u32>]) -> Vec<&fxp::ExecutionReport> {
    reports(o)
        .into_iter()
        .filter(|r| r.execution_type == fxp::ExecutionType::ExecFilled as i32)
        .collect()
}
fn cancel(id: &str) -> CancelMsg<u32> {
    CancelMsg {
        order_id: id.into(),
        sender: "owner".into(),
        client_cancel_id: "cancel".into(),
        output_tx: 7,
    }
}
fn replace(id: &str, new: &str, price: i64, tif: fxp::TimeInForce) -> CancelReplaceMsg<u32> {
    CancelReplaceMsg {
        order_id: id.into(),
        sender: "owner".into(),
        new_order_id: new.into(),
        client_replace_id: new.into(),
        new_price: price,
        new_quantity: 0,
        new_stop_price: 0,
        new_time_in_force: tif as i32,
        output_tx: 7,
    }
}
fn rejected(o: &[Output<u32>], reason: &str) {
    let r = reports(o);
    assert_eq!(r.len(), 1);
    assert_eq!(r[0].execution_type, fxp::ExecutionType::ExecRejected as i32);
    assert!(
        r[0].reject_reason.contains(reason),
        "{}",
        r[0].reject_reason
    );
}
#[test]
fn full_schedule_is_strict_and_initial_phase_is_clock_derived() {
    for (time, phase) in [
        (0, fxp::TradingPhase::Closed),
        (10, fxp::TradingPhase::OpeningCall),
        (20, fxp::TradingPhase::Continuous),
        (30, fxp::TradingPhase::ClosingCall),
        (40, fxp::TradingPhase::Closed),
    ] {
        assert_eq!(setup(time).0.phase(), phase);
    }
    assert!("close=16:00".parse::<SessionSchedule>().is_ok());
    assert!(SCHEDULE.parse::<SessionSchedule>().is_ok());
    for bad in [
        "open=09:00,close=16:00",
        "open_call=08:00,open=09:00,close=16:00",
        "open_call=09:00,open=09:00,close_call=15:00,close=16:00",
        "open_call=10:00,open=09:00,close_call=15:00,close=16:00",
        "open_call=08:00,open=09:00,close_call=17:00,close=16:00",
        "open_call=08:00,open=09:00,close_call=15:00,close=16:00,open=10:00",
    ] {
        assert!(bad.parse::<SessionSchedule>().is_err(), "{bad}");
    }
}
#[test]
fn calls_accept_crossed_limits_and_deduplicate_indicatives() {
    let (mut e, _) = setup(11);
    let first = e.new_order(buy("B", fxp::TimeInForce::Day, 10, 100), 7);
    assert!(fills(&first).is_empty());
    let second = e.new_order(sell("S", fxp::TimeInForce::Gtc, 4, 100), 8);
    assert!(fills(&second).is_empty());
    assert_eq!(e.book.order_lookup.len(), 2);
    let i = infos(&second)[0];
    assert_eq!(
        (
            i.indicative_price,
            i.indicative_volume,
            i.imbalance_quantity,
            i.imbalance_side
        ),
        (100, 4, 6, fxp::OrderSide::Buy as i32)
    );
    let no_change = e.cancel(cancel("missing"));
    assert!(infos(&no_change).is_empty());
    let parked = e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Stop as i32,
            stop_price: 100,
            ..buy("STOP", fxp::TimeInForce::Gtc, 1, 100)
        },
        7,
    );
    assert!(infos(&parked).is_empty());
    let removed = e.cancel(cancel("B"));
    let i = infos(&removed)[0];
    assert_eq!(
        (
            i.indicative_price,
            i.indicative_volume,
            i.imbalance_quantity,
            i.imbalance_side
        ),
        (0, 0, 4, fxp::OrderSide::Sell as i32)
    );
    assert!(fills(&removed).is_empty());
}
#[test]
fn opening_uncross_flags_fills_cancels_at_open_and_continuous_remainder_matches() {
    let (mut e, clock) = setup(11);
    e.new_order(buy("B", fxp::TimeInForce::Day, 10, 100), 7);
    e.new_order(sell("S", fxp::TimeInForce::AtOpen, 4, 100), 8);
    e.new_order(buy("A", fxp::TimeInForce::AtOpen, 3, 90), 7);
    advance(&clock, 20);
    let out = e.tick();
    assert_eq!(e.phase(), fxp::TradingPhase::Continuous);
    let f = fills(&out);
    assert_eq!(f.len(), 2);
    for r in f {
        assert!(r.auction);
        assert_eq!((r.execution_price, r.executed_quantity), (100, 4));
    }
    assert!(
        reports(&out)
            .iter()
            .any(|r| r.order_id == "A"
                && r.execution_type == fxp::ExecutionType::ExecCanceled as i32)
    );
    assert_eq!(e.book.order_lookup["B"].quantity, 6);
    assert!(!e.states.contains_key("A"));
    assert!(
        !out.iter()
            .filter(|o| matches!(o, Output::MarketData(_)))
            .collect::<Vec<_>>()
            .is_empty()
    );
    let continuous = e.new_order(sell("C", fxp::TimeInForce::Day, 2, 100), 8);
    assert_eq!(fills(&continuous).len(), 2);
    assert!(fills(&continuous).iter().all(|r| !r.auction));
    assert_eq!(e.states["B"].cum_qty, 6);
}
#[test]
fn call_markets_cancel_remainders_even_without_a_cross() {
    for with_liquidity in [false, true] {
        let (mut e, clock) = setup(11);
        let ack = e.new_order(
            fxp::NewOrderSingle {
                order_type: fxp::OrderType::Market as i32,
                ..buy("M", fxp::TimeInForce::Day, 10, 0)
            },
            7,
        );
        assert_eq!(
            reports(&ack)[0].execution_type,
            fxp::ExecutionType::ExecNew as i32
        );
        assert!(!ack.iter().any(|o| matches!(o, Output::MarketData(_))));
        if with_liquidity {
            e.new_order(sell("S", fxp::TimeInForce::Gtc, 4, 100), 8);
        }
        advance(&clock, 20);
        let out = e.tick();
        let r = reports(&out)
            .into_iter()
            .find(|r| {
                r.order_id == "M" && r.execution_type == fxp::ExecutionType::ExecCanceled as i32
            })
            .unwrap();
        assert_eq!(r.cum_quantity, if with_liquidity { 4 } else { 0 });
        assert_eq!(r.leaves_quantity, 0);
        assert!(!e.states.contains_key("M"));
    }
}
#[test]
fn opening_print_elects_stops_only_after_continuous_status() {
    let (mut e, clock) = setup(11);
    e.last_trade_price = Some(100);
    e.new_order(buy("B", fxp::TimeInForce::Gtc, 4, 100), 7);
    e.new_order(sell("S", fxp::TimeInForce::AtOpen, 4, 100), 8);
    e.new_order(sell("LIQ", fxp::TimeInForce::Gtc, 5, 105), 8);
    let parked = e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Stop as i32,
            stop_price: 100,
            ..buy("STOP", fxp::TimeInForce::Gtc, 2, 0)
        },
        7,
    );
    assert!(fills(&parked).is_empty());
    assert!(e.stop_book.contains("STOP"));
    advance(&clock, 20);
    let out = e.tick();
    let status = out.iter().position(|o| matches!(o, Output::TradingSessionStatus(s) if s.phase == fxp::TradingPhase::Continuous as i32)).unwrap();
    let stop = out.iter().position(|o| matches!(o, Output::ExecutionReport(_, r) if r.order_id == "STOP" && r.execution_type == fxp::ExecutionType::ExecFilled as i32)).unwrap();
    assert!(status < stop);
    assert!(
        fills(&out)
            .iter()
            .any(|r| r.order_id == "STOP" && !r.auction && r.execution_price == 105)
    );
    assert!(!e.stop_book.contains("STOP"));
}
#[test]
fn auction_tifs_are_rejected_outside_their_allowed_sessions() {
    let (mut e, clock) = setup(21);
    rejected(
        &e.new_order(buy("A", fxp::TimeInForce::AtOpen, 2, 100), 7),
        "opening auction not in progress",
    );
    for schedule in [None, Some("close=00:00:40".parse().unwrap())] {
        let mut e = Engine::new("TEST", clock.clone(), Ids(0), schedule);
        for tif in [fxp::TimeInForce::AtOpen, fxp::TimeInForce::AtClose] {
            rejected(
                &e.new_order(buy("A", tif, 2, 100), 7),
                "no auction scheduled",
            );
        }
    }
    for tif in [fxp::TimeInForce::Ioc, fxp::TimeInForce::Fok] {
        let (mut e, _) = setup(11);
        rejected(&e.new_order(buy("I", tif, 2, 100), 7), "auction");
    }
}
#[test]
fn held_at_close_joins_behind_resting_orders_and_is_cancelable_but_not_replaceable() {
    let (mut e, clock) = setup(21);
    let held = e.new_order(buy("H", fxp::TimeInForce::AtClose, 6, 100), 7);
    assert_eq!(
        reports(&held)[0].execution_type,
        fxp::ExecutionType::ExecNew as i32
    );
    assert!(!held.iter().any(|o| matches!(o, Output::MarketData(_))));
    rejected(
        &e.cancel_replace(replace("H", "H2", 101, fxp::TimeInForce::Unspecified)),
        "held",
    );
    e.new_order(buy("CANCEL", fxp::TimeInForce::AtClose, 2, 100), 7);
    let denied = e.cancel(CancelMsg {
        sender: "other".into(),
        ..cancel("CANCEL")
    });
    rejected(&denied, "Unknown order");
    let canceled = e.cancel(cancel("CANCEL"));
    assert_eq!(
        reports(&canceled)[0].execution_type,
        fxp::ExecutionType::ExecCanceled as i32
    );
    e.new_order(buy("B", fxp::TimeInForce::Gtc, 4, 100), 7);
    e.new_order(sell("S", fxp::TimeInForce::AtClose, 5, 100), 8);
    advance(&clock, 30);
    let joined = e.tick();
    assert!(
        matches!(&joined[0], Output::TradingSessionStatus(s) if s.phase == fxp::TradingPhase::ClosingCall as i32)
    );
    assert!(e.book.arrival["B"] < e.book.arrival["H"]);
    advance(&clock, 40);
    let out = e.tick();
    let buyers: Vec<_> = fills(&out)
        .into_iter()
        .filter(|r| r.side == fxp::OrderSide::Buy as i32)
        .map(|r| (r.order_id.as_str(), r.executed_quantity))
        .collect();
    assert_eq!(buyers, [("B", 4), ("H", 1)]);
    assert!(reports(&out).iter().any(|r| r.order_id == "H"
        && r.execution_type == fxp::ExecutionType::ExecCanceled as i32
        && r.cum_quantity == 1));
    assert!(e.states.is_empty());
}
#[test]
fn closing_uncross_precedes_day_expiry_and_does_not_trigger_stops() {
    let (mut e, clock) = setup(21);
    e.new_order(buy("D", fxp::TimeInForce::Day, 10, 100), 7);
    e.new_order(sell("S", fxp::TimeInForce::AtClose, 4, 100), 8);
    e.new_order(
        fxp::NewOrderSingle {
            order_type: fxp::OrderType::Stop as i32,
            stop_price: 100,
            ..buy("STOP", fxp::TimeInForce::Gtc, 2, 0)
        },
        7,
    );
    advance(&clock, 40);
    let out = e.tick();
    let fill = out
        .iter()
        .position(|o| matches!(o, Output::ExecutionReport(_, r) if r.order_id == "D" && r.auction))
        .unwrap();
    let expiry = out.iter().position(|o| matches!(o, Output::ExecutionReport(_, r) if r.order_id == "D" && r.execution_type == fxp::ExecutionType::ExecExpired as i32)).unwrap();
    let closed = out.iter().position(|o| matches!(o, Output::TradingSessionStatus(s) if s.phase == fxp::TradingPhase::Closed as i32)).unwrap();
    assert!(fill < closed && closed < expiry);
    let r = reports(&out)
        .into_iter()
        .find(|r| r.order_id == "D" && r.execution_type == fxp::ExecutionType::ExecExpired as i32)
        .unwrap();
    assert_eq!((r.cum_quantity, r.avg_px, r.leaves_quantity), (4, 100, 0));
    assert!(e.stop_book.contains("STOP"));
    assert_eq!(e.last_trade_price, Some(100));
}
#[test]
fn closed_rejects_entry_and_replace_but_allows_cancel() {
    let (mut e, clock) = setup(21);
    e.new_order(buy("B", fxp::TimeInForce::Gtc, 10, 100), 7);
    advance(&clock, 40);
    e.tick();
    rejected(
        &e.new_order(buy("N", fxp::TimeInForce::Gtc, 1, 100), 7),
        "market closed",
    );
    rejected(
        &e.cancel_replace(replace("B", "B2", 101, fxp::TimeInForce::Unspecified)),
        "market closed",
    );
    let out = e.cancel(cancel("B"));
    assert_eq!(
        reports(&out)[0].execution_type,
        fxp::ExecutionType::ExecCanceled as i32
    );
}
#[test]
fn replace_in_call_updates_indicative_without_matching() {
    let (mut e, _) = setup(11);
    e.new_order(buy("B", fxp::TimeInForce::Day, 10, 90), 7);
    e.new_order(sell("S", fxp::TimeInForce::Gtc, 4, 100), 8);
    let out = e.cancel_replace(replace("B", "B2", 100, fxp::TimeInForce::Unspecified));
    assert!(fills(&out).is_empty());
    assert_eq!(reports(&out).len(), 2);
    assert_eq!(infos(&out)[0].indicative_volume, 4);
    assert_eq!(e.book.order_lookup.len(), 2);
}
#[test]
fn multi_day_jump_processes_each_auction_once() {
    let (mut e, clock) = setup(11);
    e.new_order(buy("B", fxp::TimeInForce::Gtc, 4, 100), 7);
    e.new_order(sell("S", fxp::TimeInForce::Gtc, 4, 100), 8);
    advance(&clock, 2 * 86400 + 40);
    let out = e.tick();
    assert_eq!(fills(&out).len(), 2);
    let phases: Vec<_> = out
        .iter()
        .filter_map(|o| match o {
            Output::TradingSessionStatus(s) => Some(s.phase),
            _ => None,
        })
        .collect();
    assert_eq!(
        phases
            .iter()
            .filter(|p| **p == fxp::TradingPhase::Continuous as i32)
            .count(),
        3
    );
    assert_eq!(
        phases
            .iter()
            .filter(|p| **p == fxp::TradingPhase::Closed as i32)
            .count(),
        3
    );
    assert_eq!(
        phases
            .iter()
            .filter(|p| **p == fxp::TradingPhase::ClosingCall as i32)
            .count(),
        3
    );
    assert_eq!(
        phases
            .iter()
            .filter(|p| **p == fxp::TradingPhase::OpeningCall as i32)
            .count(),
        2
    );
    assert!(e.tick().is_empty());
}
