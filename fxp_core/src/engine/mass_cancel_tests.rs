// SPDX-License-Identifier: MIT
//! Owner-scoped mass cancel (explicit requests and cancel-on-disconnect).
use super::*;
use crate::event_log::{Input, Journal, tests::TestDir};
use crate::order_book::order::StpMode;
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
fn limit(id: &str, owner: &str, side: fxp::OrderSide, price: i64) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        sender: owner.into(),
        symbol: "TEST".into(),
        side: side as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        price,
        quantity: 5,
        ..Default::default()
    }
}
fn stop(id: &str, owner: &str) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_type: fxp::OrderType::Stop as i32,
        stop_price: 200,
        price: 0,
        ..limit(id, owner, fxp::OrderSide::Buy, 0)
    }
}
fn request(owner: &str, side: fxp::OrderSide, reason: fxp::MassCancelReason) -> MassCancelMsg {
    MassCancelMsg {
        sender: owner.into(),
        side: side as i32,
        client_request_id: "MC1".into(),
        reason: reason as i32,
    }
}
fn canceled(out: &[Output<u32>]) -> Vec<(u32, fxp::ExecutionReport)> {
    out.iter()
        .filter_map(|o| match o {
            Output::ExecutionReport(tx, r)
                if r.execution_type == fxp::ExecutionType::ExecCanceled as i32 =>
            {
                Some((*tx, r.clone()))
            }
            _ => None,
        })
        .collect()
}
fn count(out: &[Output<u32>]) -> u32 {
    let counts: Vec<_> = out
        .iter()
        .filter_map(|o| match o {
            Output::MassCancelCount(n) => Some(*n),
            _ => None,
        })
        .collect();
    assert_eq!(counts.len(), 1, "exactly one count per mass cancel");
    counts[0]
}
fn seeded() -> Engine<u32, FakeClock, Ids> {
    let mut e = engine();
    e.new_order(limit("T-B", "T", fxp::OrderSide::Buy, 99), 1);
    e.new_order(limit("T-S", "T", fxp::OrderSide::Sell, 101), 1);
    e.new_order(stop("T-STOP", "T"), 1);
    e.new_order(limit("U-B", "U", fxp::OrderSide::Buy, 98), 2);
    e
}

#[test]
fn mass_cancel_cancels_all_of_the_owners_orders_including_stops() {
    let mut e = seeded();
    let out = e.mass_cancel(request(
        "T",
        fxp::OrderSide::Unspecified,
        fxp::MassCancelReason::Request,
    ));
    assert_eq!(count(&out), 3);
    let c = canceled(&out);
    let ids: Vec<_> = c.iter().map(|(_, r)| r.order_id.as_str()).collect();
    assert_eq!(ids, ["T-B", "T-S", "T-STOP"], "arrival order");
    for (tx, r) in &c {
        assert_eq!(*tx, 1, "reports go to the order owner's connection");
        assert_eq!(r.order_status, fxp::OrderStatus::OrderCanceled as i32);
        assert_eq!(
            r.client_order_id, "",
            "each ER keeps the order's own id, not the request id MC1"
        );
        assert_eq!(r.reject_reason, "mass cancel");
    }
    assert!(e.stop_book.is_empty());
    assert_eq!(e.book.order_lookup.len(), 1);
    assert!(e.book.order_lookup.contains_key("U-B"));
    // Book cancels publish market data; the parked stop does not.
    let md = out
        .iter()
        .filter(|o| matches!(o, Output::MarketData(_)))
        .count();
    assert_eq!(md, 2);
}

#[test]
fn mass_cancel_is_scoped_to_the_requesting_owner() {
    let mut e = seeded();
    let out = e.mass_cancel(request(
        "ATTACKER",
        fxp::OrderSide::Unspecified,
        fxp::MassCancelReason::Request,
    ));
    assert_eq!(count(&out), 0);
    assert!(canceled(&out).is_empty());
    assert_eq!(e.book.order_lookup.len(), 3);
    assert_eq!(e.stop_book.len(), 1);
}

#[test]
fn mass_cancel_side_filter() {
    let mut e = seeded();
    let out = e.mass_cancel(request(
        "T",
        fxp::OrderSide::Buy,
        fxp::MassCancelReason::Request,
    ));
    assert_eq!(count(&out), 2);
    let ids: Vec<_> = canceled(&out)
        .into_iter()
        .map(|(_, r)| r.order_id)
        .collect();
    assert_eq!(ids, ["T-B", "T-STOP"]);
    assert!(e.book.order_lookup.contains_key("T-S"));
}

#[test]
fn mass_cancel_with_nothing_live_reports_zero() {
    let mut e = engine();
    let out = e.mass_cancel(request(
        "T",
        fxp::OrderSide::Unspecified,
        fxp::MassCancelReason::Request,
    ));
    assert_eq!(count(&out), 0);
    assert!(canceled(&out).is_empty());
}

#[test]
fn cancel_on_disconnect_uses_a_disconnect_reason_and_keeps_order_ids() {
    let mut e = seeded();
    let out = e.mass_cancel(MassCancelMsg {
        client_request_id: String::new(),
        ..request(
            "T",
            fxp::OrderSide::Unspecified,
            fxp::MassCancelReason::Disconnect,
        )
    });
    assert_eq!(count(&out), 3);
    for (_, r) in canceled(&out) {
        assert_eq!(r.reject_reason, "cancel on disconnect");
        assert_eq!(r.client_order_id, "", "no request id: keep the order's own");
    }
}

#[test]
fn mass_cancel_includes_orders_held_for_the_closing_auction() {
    let clock = FakeClock(Rc::new(Cell::new(Time {
        seconds: 21,
        nanos: 0,
    })));
    let schedule = "open_call=00:00:10,open=00:00:20,close_call=00:00:30,close=00:00:40";
    let mut e = Engine::new("TEST", clock, Ids(0), Some(schedule.parse().unwrap()));
    e.new_order(
        fxp::NewOrderSingle {
            time_in_force: fxp::TimeInForce::AtClose as i32,
            ..limit("AC", "T", fxp::OrderSide::Buy, 100)
        },
        1,
    );
    let out = e.mass_cancel(request(
        "T",
        fxp::OrderSide::Unspecified,
        fxp::MassCancelReason::Request,
    ));
    assert_eq!(count(&out), 1);
    assert!(e.held_close.is_empty());
    assert!(e.states.is_empty());
}

#[test]
fn mass_cancel_is_journaled_and_replayed() {
    let dir = TestDir::new();
    let mut journal = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    let at = Time {
        seconds: 1,
        nanos: 0,
    };
    journal
        .apply(
            "TEST",
            at,
            Input::New(limit("T-B", "T", fxp::OrderSide::Buy, 99)),
            1,
        )
        .unwrap();
    journal
        .apply("TEST", at, Input::New(stop("T-STOP", "T")), 1)
        .unwrap();
    let out = journal
        .apply(
            "TEST",
            at,
            Input::MassCancel(fxp::OrderMassCancelRequest {
                symbol: "TEST".into(),
                sender: "T".into(),
                reason: fxp::MassCancelReason::Disconnect as i32,
                ..Default::default()
            }),
            1,
        )
        .unwrap();
    assert_eq!(count(&out), 2);
    drop(journal);
    let replayed = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    let engine = &replayed.engines["TEST"];
    assert!(engine.book.order_lookup.is_empty());
    assert!(engine.stop_book.is_empty());
    assert!(engine.states.is_empty());
}

#[test]
fn session_control_is_journaled_and_replayed() {
    let dir = TestDir::new();
    let mut journal = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    let at = Time {
        seconds: 1,
        nanos: 0,
    };
    journal
        .control(
            "TEST",
            at,
            Input::SessionControl(fxp::SessionControl {
                sender: "op".into(),
                opening_call_seconds: 60,
                ..Default::default()
            }),
        )
        .unwrap();
    assert_eq!(
        journal.engines["TEST"].phase(),
        fxp::TradingPhase::OpeningCall
    );
    drop(journal);
    let replayed = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    let engine = &replayed.engines["TEST"];
    assert_eq!(engine.phase(), fxp::TradingPhase::OpeningCall);
    assert_eq!(
        engine.call_until,
        Some(Time {
            seconds: 61,
            nanos: 0
        })
    );
}
