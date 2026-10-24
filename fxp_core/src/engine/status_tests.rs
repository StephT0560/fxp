// SPDX-License-Identifier: MIT
use super::*;
use std::cell::Cell;
use std::rc::Rc;
#[derive(Clone)]
struct TestClock(Rc<Cell<Time>>);
impl Clock for TestClock {
    fn now(&self) -> Time {
        self.0.get()
    }
}
struct Ids(u64);
impl ExecIds for Ids {
    fn next_id(&mut self) -> String {
        self.0 += 1;
        format!("S{}", self.0)
    }
}
fn order(id: &str, owner: &str, side: fxp::OrderSide, qty: i32) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        order_id: id.into(),
        sender: owner.into(),
        symbol: "TEST".into(),
        side: side as i32,
        quantity: qty,
        price: 100,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}
#[test]
fn status_lifecycle_and_owner_isolation() {
    let clock = TestClock(Rc::new(Cell::new(Time {
        seconds: 0,
        nanos: 0,
    })));
    let mut e = Engine::new(
        "TEST",
        clock.clone(),
        Ids(0),
        Some("close=00:01".parse().unwrap()),
    );
    e.new_order(order("B", "T", fxp::OrderSide::Buy, 10), 1);
    let status = |e: &Engine<u32, TestClock, Ids>, id: &str| {
        let v = e.order_status("T", id, 0);
        assert_eq!(v.len(), 1);
        v[0].clone()
    };
    assert_eq!(
        status(&e, "B").order_status,
        fxp::OrderStatus::OrderNew as i32
    );
    e.new_order(order("S", "U", fxp::OrderSide::Sell, 4), 2);
    let r = status(&e, "B");
    assert_eq!(
        (r.order_status, r.cum_quantity, r.leaves_quantity),
        (fxp::OrderStatus::OrderPartiallyFilled as i32, 4, 6)
    );
    assert!(e.order_status("U", "B", 0).is_empty());
    assert!(e.order_status("T", "missing", 0).is_empty());
    e.new_order(order("S2", "U", fxp::OrderSide::Sell, 6), 2);
    let r = status(&e, "B");
    assert_eq!(
        (r.order_status, r.cum_quantity, r.leaves_quantity),
        (fxp::OrderStatus::OrderFilled as i32, 10, 0)
    );
    e.new_order(order("C", "T", fxp::OrderSide::Buy, 1), 3); // same owner, new transport/session
    e.cancel(CancelMsg {
        order_id: "C".into(),
        sender: "T".into(),
        client_cancel_id: "cancel".into(),
        output_tx: 3,
    });
    assert_eq!(
        status(&e, "C").order_status,
        fxp::OrderStatus::OrderCanceled as i32
    );
    let mut stop = order("STOP", "T", fxp::OrderSide::Buy, 2);
    stop.order_type = fxp::OrderType::Stop as i32;
    stop.stop_price = 200;
    e.new_order(stop, 3);
    assert_eq!(
        status(&e, "STOP").order_status,
        fxp::OrderStatus::OrderNew as i32
    );
    let mut day = order("DAY", "T", fxp::OrderSide::Buy, 2);
    day.time_in_force = fxp::TimeInForce::Day as i32;
    e.new_order(day, 3);
    clock.0.set(Time {
        seconds: 60,
        nanos: 0,
    });
    e.tick();
    assert_eq!(
        status(&e, "DAY").order_status,
        fxp::OrderStatus::OrderExpired as i32
    );
    let all = e.order_status("T", "", 0);
    assert_eq!(all.len(), 4);
    assert!(
        all.iter()
            .all(|r| !r.order_id.starts_with('S') || r.order_id == "STOP")
    );
}

#[test]
fn reused_terminal_id_does_not_retain_the_previous_owner() {
    let clock = TestClock(Rc::new(Cell::new(Time {
        seconds: 0,
        nanos: 0,
    })));
    let mut e = Engine::new("TEST", clock, Ids(0), None);
    e.new_order(order("REUSE", "T", fxp::OrderSide::Buy, 1), 1);
    e.cancel(CancelMsg {
        order_id: "REUSE".into(),
        sender: "T".into(),
        client_cancel_id: "C".into(),
        output_tx: 1,
    });
    e.new_order(order("REUSE", "U", fxp::OrderSide::Buy, 9), 2);
    assert!(
        e.order_status("T", "REUSE", 0).is_empty(),
        "previous owner can see the new order"
    );
    assert_eq!(e.order_status("U", "REUSE", 0)[0].order_quantity, 9);
}
