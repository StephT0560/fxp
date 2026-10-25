// SPDX-License-Identifier: MIT
use super::*;
use crate::order_book::order::{OrderSide, OrderType, StpMode, TimeInForce};
use crate::test_helpers::{make_book, make_order};
fn iceberg(id: &str, qty: u32, display: u32) -> order::Order {
    let mut o = make_order(
        id,
        OrderSide::Sell,
        100,
        qty,
        OrderType::Limit,
        TimeInForce::GTC,
    );
    o.display_quantity = display;
    o.trader_id = "maker".into();
    o
}
#[test]
fn iceberg_slices_replenish_at_the_back_of_the_level() {
    let mut b = make_book();
    b.add_order(iceberg("A", 6, 2));
    b.add_order(iceberg("B", 4, 2));
    b.add_order(iceberg("C", 2, 0));
    assert_eq!(b.get_market_snapshot().asks, vec![(100, 6)]);
    let incoming = make_order(
        "BUY",
        OrderSide::Buy,
        0,
        8,
        OrderType::Market,
        TimeInForce::IOC,
    );
    let result = b.process_aggressive(incoming);
    assert_eq!(
        result
            .fills
            .iter()
            .map(|r| (r.sell_order_id.as_str(), r.trade_quantity))
            .collect::<Vec<_>>(),
        vec![("A", 2), ("B", 2), ("C", 2), ("A", 2)]
    );
    assert_eq!(b.order_lookup["A"].quantity, 2);
    assert_eq!(b.order_lookup["B"].quantity, 2);
    assert_eq!(b.get_market_snapshot().asks, vec![(100, 4)]);
    let md = b.cancel_order("A").unwrap();
    assert_eq!(md.changes[0].quantity, 2);
    assert_eq!(b.get_market_snapshot().asks, vec![(100, 2)]);
}
#[test]
fn fok_preflight_respects_replenishment_before_self_trade() {
    let mut b = make_book();
    b.add_order(iceberg("OTHER", 10, 2));
    let mut own = iceberg("OWN", 1, 0);
    own.trader_id = "taker".into();
    b.add_order(own);
    let mut taker = make_order(
        "FOK",
        OrderSide::Buy,
        100,
        5,
        OrderType::Limit,
        TimeInForce::FOK,
    );
    taker.trader_id = "taker".into();
    taker.stp.mode = StpMode::CancelNewest;
    let result = b.process_aggressive(taker);
    assert!(result.fills.is_empty());
    assert_eq!(b.order_lookup["OTHER"].quantity, 10);
    assert_eq!(b.get_market_snapshot().asks, vec![(100, 3)]);
}

#[test]
fn iceberg_replace_resets_slice_and_priority() {
    use crate::order_book::order_book::OrderReplace;
    let mut b = make_book();
    b.add_order(iceberg("A", 10, 3));
    b.add_order(iceberg("B", 10, 2));
    b.process_aggressive(make_order(
        "T",
        OrderSide::Buy,
        0,
        1,
        OrderType::Market,
        TimeInForce::IOC,
    ));
    assert_eq!(b.visible(&b.order_lookup["A"]), 2);
    b.replace_order(
        "A",
        "R",
        OrderReplace {
            new_price: 0,
            new_total_qty: 8,
            new_display_quantity: None,
            new_time_in_force: None,
            client_order_id: "R",
            timestamp: 1,
        },
        "maker",
    )
    .unwrap();
    assert_eq!(b.visible(&b.order_lookup["R"]), 3);
    assert_eq!(b.order_lookup["R"].quantity, 7);
    let r = b.process_aggressive(make_order(
        "T2",
        OrderSide::Buy,
        0,
        5,
        OrderType::Market,
        TimeInForce::IOC,
    ));
    assert_eq!(
        r.fills
            .iter()
            .map(|f| (f.sell_order_id.as_str(), f.trade_quantity))
            .collect::<Vec<_>>(),
        vec![("B", 2), ("R", 3)]
    );
}

#[test]
fn iceberg_stp_cancels_reserve_and_decrements_reserve_first() {
    for mode in [
        StpMode::CancelNewest,
        StpMode::CancelOldest,
        StpMode::CancelBoth,
        StpMode::DecrementAndCancel,
    ] {
        let mut b = make_book();
        b.add_order(iceberg("A", 10, 3));
        let mut o = make_order(
            "T",
            OrderSide::Buy,
            100,
            2,
            OrderType::Limit,
            TimeInForce::IOC,
        );
        o.trader_id = "maker".into();
        o.stp.mode = mode;
        let r = b.process_aggressive(o);
        assert!(r.fills.is_empty());
        if matches!(mode, StpMode::CancelOldest | StpMode::CancelBoth) {
            assert!(b.order_lookup.is_empty());
            assert_eq!(b.stp.updates[0].changes[0].quantity, 3);
        } else {
            assert_eq!(
                b.order_lookup["A"].quantity,
                if mode == StpMode::CancelNewest { 10 } else { 8 }
            );
            assert_eq!(b.get_market_snapshot().asks, vec![(100, 3)]);
        }
    }
}
#[test]
fn ordinary_fok_can_fill_several_reserve_slices() {
    let mut b = make_book();
    b.add_order(iceberg("A", 7, 2));
    let r = b.process_aggressive(make_order(
        "T",
        OrderSide::Buy,
        100,
        7,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(
        r.fills.iter().map(|f| f.trade_quantity).collect::<Vec<_>>(),
        vec![2, 2, 2, 1]
    );
    assert!(b.order_lookup.is_empty());
}
