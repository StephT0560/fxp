// SPDX-License-Identifier: MIT
use super::order::{OrderSide, OrderType, TimeInForce};
use super::stops::{Execution, StopBook, run_stop_cascade};
use crate::test_helpers::{make_book, make_order};

/// P09 replacement: a transient qualifying print must elect the stop even
/// though the original sweep's final print is back above its sell trigger.
#[test]
fn p09_cascade_uses_every_print_not_only_the_last_print() {
    let mut book = make_book();
    let mut stops = StopBook::default();
    let mut last = None;
    for (id, side, price, qty) in [
        ("B", OrderSide::Buy, 98, 3),
        ("S1", OrderSide::Sell, 99, 1),
        ("S2", OrderSide::Sell, 105, 1),
    ] {
        book.add_order(make_order(
            id,
            side,
            price,
            qty,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
    }
    let mut stop = make_order(
        "STOP",
        OrderSide::Sell,
        0,
        1,
        OrderType::Stop,
        TimeInForce::GTC,
    );
    stop.stop_price = 100;
    stops.park(stop);
    let mut execution = Execution::default();
    book.execute_into(
        make_order(
            "TAKER",
            OrderSide::Buy,
            0,
            2,
            OrderType::Market,
            TimeInForce::IOC,
        ),
        &mut execution,
    );
    assert_eq!(
        execution
            .fills
            .iter()
            .map(|f| f.trade_price)
            .collect::<Vec<_>>(),
        vec![99, 105]
    );
    run_stop_cascade(&mut book, &mut stops, &mut last, &mut execution);
    assert_eq!(execution.triggered, vec!["STOP"]);
    assert_eq!(
        execution
            .fills
            .iter()
            .map(|f| (f.sell_order_id.as_str(), f.trade_price, f.trade_quantity))
            .collect::<Vec<_>>(),
        vec![("S1", 99, 1), ("S2", 105, 1), ("STOP", 98, 1)]
    );
    assert!(stops.is_empty());
    assert_eq!(last, Some(98));
    assert_eq!(book.order_lookup["B"].quantity, 2);
}
