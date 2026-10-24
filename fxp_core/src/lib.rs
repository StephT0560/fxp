// SPDX-License-Identifier: MIT

pub mod engine;
mod event_log;
pub mod order_book;
pub mod server;

#[allow(clippy::doc_lazy_continuation, clippy::doc_overindented_list_items)]
pub mod fxp {
    include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
}

pub const PROTOCOL_VERSION: &str = "2.3";

#[test]
fn protocol_version_is_v2() {
    assert_eq!(PROTOCOL_VERSION, "2.3");
}

// ── Shared test helpers ───────────────────────────────────────────────────────

#[cfg(test)]
mod test_helpers {
    use crate::order_book::broadcaster::MarketDataBroadcaster;
    use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use std::sync::Arc;

    pub fn make_book() -> OrderBook {
        OrderBook::new("AAPL", Arc::new(MarketDataBroadcaster::new()))
    }

    pub fn make_order(
        id: &str,
        side: OrderSide,
        price: i64,
        qty: u32,
        order_type: OrderType,
        tif: TimeInForce,
    ) -> Order {
        Order {
            order_id: id.to_string(),
            trader_id: "T".to_string(),
            symbol: "AAPL".to_string(),
            price,
            quantity: qty,
            side,
            order_type,
            time_in_force: tif,
            timestamp: 0,
            stop_price: 0,
            expire_date: String::new(),
            account: String::new(),
            client_order_id: String::new(),
            stp: Default::default(),
        }
    }

    /// Seed book: sells at 101_00 (100 qty) and 102_00 (100 qty)
    ///            buys  at  99_00 (100 qty) and  98_00 (100 qty)
    pub fn seed_book(book: &mut OrderBook) {
        for (id, price) in [("SEED-S1", 10_100_i64), ("SEED-S2", 10_200_i64)] {
            book.add_order(make_order(
                id,
                OrderSide::Sell,
                price,
                100,
                OrderType::Limit,
                TimeInForce::GTC,
            ));
        }
        for (id, price) in [("SEED-B1", 99_00i64), ("SEED-B2", 98_00i64)] {
            book.add_order(make_order(
                id,
                OrderSide::Buy,
                price,
                100,
                OrderType::Limit,
                TimeInForce::GTC,
            ));
        }
    }
}

// ── Lifecycle test ────────────────────────────────────────────────────────────

#[test]
fn test_full_order_lifecycle() {
    use crate::order_book::broadcaster::MarketDataBroadcaster;
    use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use std::sync::Arc;

    let broadcaster = Arc::new(MarketDataBroadcaster::new());
    let mut book = OrderBook::new("AAPL", broadcaster.clone());
    broadcaster.subscribe(1, "AAPL");

    book.add_order(Order {
        order_id: "B1".to_string(),
        trader_id: "TraderA".to_string(),
        symbol: "AAPL".to_string(),
        price: 10_100,
        quantity: 10,
        side: OrderSide::Buy,
        order_type: OrderType::Limit,
        time_in_force: TimeInForce::GTC,
        timestamp: 1700000000000,
        stop_price: 0,
        expire_date: String::new(),
        account: String::new(),
        client_order_id: String::new(),
        stp: Default::default(),
    });

    book.add_order(Order {
        order_id: "S1".to_string(),
        trader_id: "TraderB".to_string(),
        symbol: "AAPL".to_string(),
        price: 10_100,
        quantity: 10,
        side: OrderSide::Sell,
        order_type: OrderType::Limit,
        time_in_force: TimeInForce::GTC,
        timestamp: 1700000000001,
        stop_price: 0,
        expire_date: String::new(),
        account: String::new(),
        client_order_id: String::new(),
        stp: Default::default(),
    });

    let (fills, updates) = book.match_orders();
    assert_eq!(fills.len(), 1);
    assert_eq!(fills[0].trade_price, 10_100);
    assert_eq!(fills[0].trade_quantity, 10);
    assert!(fills[0].buy_fully_filled);
    assert!(fills[0].sell_fully_filled);
    assert_eq!(fills[0].buy_order_id, "B1");
    assert_eq!(fills[0].sell_order_id, "S1");
    assert_eq!(updates.len(), 1);
    broadcaster.broadcast_snapshot(&book.get_market_snapshot());
}

// ── Market order tests ────────────────────────────────────────────────────────

#[test]
fn test_market_full_fill() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "M1",
        OrderSide::Buy,
        0,
        50,
        OrderType::Market,
        TimeInForce::Day,
    ));
    assert_eq!(r.fills.len(), 1);
    assert_eq!(r.fills[0].trade_price, 10_100);
    assert_eq!(r.fills[0].trade_quantity, 50);
    assert_eq!(r.unfilled_qty, 0);
    assert!(!r.rest_on_book);
}

#[test]
fn test_market_sweeps_multiple_levels() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "M2",
        OrderSide::Buy,
        0,
        150,
        OrderType::Market,
        TimeInForce::Day,
    ));
    assert_eq!(r.fills.len(), 2);
    assert_eq!(r.fills[0].trade_price, 10_100);
    assert_eq!(r.fills[0].trade_quantity, 100);
    assert_eq!(r.fills[1].trade_price, 10_200);
    assert_eq!(r.fills[1].trade_quantity, 50);
    assert_eq!(r.unfilled_qty, 0);
}

#[test]
fn test_market_partial_no_liquidity() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "M3",
        OrderSide::Buy,
        0,
        300,
        OrderType::Market,
        TimeInForce::Day,
    ));
    assert_eq!(r.unfilled_qty, 100);
    assert!(!r.rest_on_book);
}

#[test]
fn test_market_empty_book() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    let r = book.process_aggressive(make_order(
        "M4",
        OrderSide::Buy,
        0,
        100,
        OrderType::Market,
        TimeInForce::Day,
    ));
    assert_eq!(r.fills.len(), 0);
    assert_eq!(r.unfilled_qty, 100);
    assert!(!r.rest_on_book);
}

// ── IOC tests ─────────────────────────────────────────────────────────────────

#[test]
fn test_ioc_full_fill() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "I1",
        OrderSide::Buy,
        10_100,
        50,
        OrderType::Limit,
        TimeInForce::IOC,
    ));
    assert_eq!(r.fills.len(), 1);
    assert_eq!(r.fills[0].trade_quantity, 50);
    assert_eq!(r.unfilled_qty, 0);
    assert!(!r.rest_on_book);
}

#[test]
fn test_ioc_partial_then_cancel() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    // 150 wanted at 101_00 but only 100 available there
    let r = book.process_aggressive(make_order(
        "I2",
        OrderSide::Buy,
        10_100,
        150,
        OrderType::Limit,
        TimeInForce::IOC,
    ));
    assert_eq!(r.fills[0].trade_quantity, 100);
    assert_eq!(r.unfilled_qty, 50);
    assert!(!r.rest_on_book);
}

#[test]
fn test_ioc_no_cross() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    // Limit too low — best ask is 101_00
    let r = book.process_aggressive(make_order(
        "I3",
        OrderSide::Buy,
        10_000,
        50,
        OrderType::Limit,
        TimeInForce::IOC,
    ));
    assert_eq!(r.fills.len(), 0);
    assert_eq!(r.unfilled_qty, 50);
    assert!(!r.rest_on_book);
}

// ── FOK tests ─────────────────────────────────────────────────────────────────

#[test]
fn test_fok_full_fill() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "F1",
        OrderSide::Buy,
        10_100,
        50,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(r.fills.len(), 1);
    assert_eq!(r.unfilled_qty, 0);
    assert!(!r.rest_on_book);
}

#[test]
fn test_fok_kill_insufficient_liquidity() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    // 150 wanted at 101_00, only 100 there — kill
    let r = book.process_aggressive(make_order(
        "F2",
        OrderSide::Buy,
        10_100,
        150,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(r.fills.len(), 0);
    assert_eq!(r.unfilled_qty, 150);
    // Book must be unchanged
    let snap = book.get_market_snapshot();
    let ask_qty: u32 = snap.asks.iter().map(|(_, q)| q).sum();
    assert_eq!(ask_qty, 200);
}

#[test]
fn test_fok_kill_no_cross() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    let r = book.process_aggressive(make_order(
        "F3",
        OrderSide::Buy,
        10_000,
        10,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(r.fills.len(), 0);
    assert_eq!(r.unfilled_qty, 10);
}

#[test]
fn test_fok_fill_across_levels() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    // 150 at 102_00 — 200 available across both levels, should fill all
    let r = book.process_aggressive(make_order(
        "F4",
        OrderSide::Buy,
        10_200,
        150,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(r.fills.len(), 2);
    assert_eq!(r.unfilled_qty, 0);
}

#[test]
fn test_fok_does_not_consume_book_on_kill() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    book.add_order(make_order(
        "S1",
        OrderSide::Sell,
        10_100,
        30,
        OrderType::Limit,
        TimeInForce::GTC,
    ));
    // FOK for 50, only 30 available — kill
    let r = book.process_aggressive(make_order(
        "F5",
        OrderSide::Buy,
        10_100,
        50,
        OrderType::Limit,
        TimeInForce::FOK,
    ));
    assert_eq!(r.fills.len(), 0);
    let snap = book.get_market_snapshot();
    let ask_qty: u32 = snap.asks.iter().map(|(_, q)| q).sum();
    assert_eq!(ask_qty, 30, "Book must be unchanged after FOK kill");
}

// ── Limit resting tests ───────────────────────────────────────────────────────

#[test]
fn test_limit_gtc_rests_when_no_cross() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    let order = make_order(
        "L1",
        OrderSide::Buy,
        99_00,
        50,
        OrderType::Limit,
        TimeInForce::GTC,
    );
    let r = book.process_aggressive(order.clone());
    assert!(r.rest_on_book);
    assert_eq!(r.unfilled_qty, 50);
    assert_eq!(r.fills.len(), 0);
    // Simulate server: add resting order
    let mut resting = order;
    resting.quantity = r.unfilled_qty;
    book.add_order(resting);
    let snap = book.get_market_snapshot();
    assert!(
        snap.bids.iter().any(|(p, _)| *p == 99_00),
        "Order should be in the book"
    );
}

#[test]
fn test_limit_day_matches_on_arrival() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    seed_book(&mut book);
    // Aggressive limit buy at 102 crosses resting ask at 101
    let order = make_order(
        "L2",
        OrderSide::Buy,
        10_200,
        50,
        OrderType::Limit,
        TimeInForce::Day,
    );
    let r = book.process_aggressive(order.clone());
    assert!(r.rest_on_book);
    let mut resting = order;
    resting.quantity = r.unfilled_qty;
    book.add_order(resting);
    let (fills, _) = book.match_orders();
    assert_eq!(fills.len(), 1);
    assert_eq!(
        fills[0].trade_price, 10_100,
        "Price improvement: fills at resting ask"
    );
    assert_eq!(fills[0].trade_quantity, 50);
}

// ── Partially-filled flag tests ───────────────────────────────────────────────

#[test]
fn test_partial_fill_flags() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;
    let mut book = make_book();
    book.add_order(make_order(
        "S1",
        OrderSide::Sell,
        10_100,
        50,
        OrderType::Limit,
        TimeInForce::GTC,
    ));
    // Market buy for 100 — fills 50, leaves 50 unfilled
    let r = book.process_aggressive(make_order(
        "M1",
        OrderSide::Buy,
        0,
        100,
        OrderType::Market,
        TimeInForce::Day,
    ));
    assert_eq!(r.fills.len(), 1);
    assert!(
        !r.fills[0].buy_fully_filled,
        "Aggressor (buy 100) not fully filled"
    );
    assert!(
        r.fills[0].sell_fully_filled,
        "Resting sell (50) fully filled"
    );
    assert_eq!(r.unfilled_qty, 50);
}

// ── Stop order tests ──────────────────────────────────────────────────────────
// Note: Stop order TRIGGERING is handled in server.rs (order_book_task).
// These tests cover the matcher behaviour AFTER a stop fires:
//   - Stop (Market): should sweep the book like a market order
//   - StopLimit: should rest on the book like a limit order
// The drain_triggered_stops function is also tested directly.

#[test]
fn test_stop_fires_as_market_order() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;

    let mut book = make_book();
    seed_book(&mut book);

    // A sell stop that has triggered — process_aggressive treats it as market
    // The stop_price field is set but we're calling process_aggressive directly
    // (simulating post-trigger behaviour in order_book_task)
    let mut stop = make_order(
        "STOP-S1",
        OrderSide::Sell,
        0,
        50,
        OrderType::Stop,
        TimeInForce::Day,
    );
    stop.stop_price = 99_00; // would trigger when price drops to 99

    let r = book.process_aggressive(stop);

    // Should behave exactly like a market sell
    assert_eq!(r.fills.len(), 1);
    assert_eq!(r.fills[0].trade_price, 99_00, "Fills at best bid");
    assert_eq!(r.fills[0].trade_quantity, 50);
    assert_eq!(r.unfilled_qty, 0);
    assert!(!r.rest_on_book, "Stop never rests after triggering");
}

#[test]
fn test_stop_market_sweeps_multiple_levels() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;

    let mut book = make_book();
    seed_book(&mut book);

    // Buy stop triggered — sweeps both ask levels (101_00 and 102_00)
    let mut stop = make_order(
        "STOP-B1",
        OrderSide::Buy,
        0,
        150,
        OrderType::Stop,
        TimeInForce::Day,
    );
    stop.stop_price = 10_100;

    let r = book.process_aggressive(stop);

    assert_eq!(r.fills.len(), 2);
    assert_eq!(r.fills[0].trade_price, 10_100);
    assert_eq!(r.fills[0].trade_quantity, 100);
    assert_eq!(r.fills[1].trade_price, 10_200);
    assert_eq!(r.fills[1].trade_quantity, 50);
    assert_eq!(r.unfilled_qty, 0);
    assert!(!r.rest_on_book);
}

#[test]
fn test_stop_limit_rests_after_trigger() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;

    let mut book = make_book();
    seed_book(&mut book);

    // StopLimit sell triggered: stop_price=99, limit price=98
    // After triggering, becomes a limit sell at 98 — should rest (98 < best bid 99)
    let mut stop_limit = make_order(
        "SL-S1",
        OrderSide::Sell,
        98_00,
        50,
        OrderType::StopLimit,
        TimeInForce::GTC,
    );
    stop_limit.stop_price = 99_00;

    let r = book.process_aggressive(stop_limit.clone());

    // StopLimit returns rest_on_book=true — server adds it as limit
    assert!(
        r.rest_on_book,
        "StopLimit should rest on book after triggering"
    );
    assert_eq!(r.unfilled_qty, 50);
    assert_eq!(r.fills.len(), 0);

    // Simulate server: add as resting limit, then match
    let mut resting = stop_limit;
    resting.order_type = crate::order_book::order::OrderType::Limit;
    resting.quantity = r.unfilled_qty;
    book.add_order(resting);
    let (fills, _) = book.match_orders();

    // The StopLimit sell arrives at 98_00 and crosses the resting 99_00 bid.
    // Trades execute at the resting order's price, so the fill is at 99_00.
    assert_eq!(fills.len(), 1);
    assert_eq!(
        fills[0].trade_price, 99_00,
        "Fills at the resting bid price"
    );
    assert_eq!(fills[0].trade_quantity, 50);
}

#[test]
fn test_p03_passive_cross_at_resting_price() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    let mut book = test_helpers::make_book();
    book.add_order(test_helpers::make_order(
        "BID",
        OrderSide::Buy,
        10_100,
        10,
        OrderType::Limit,
        TimeInForce::GTC,
    ));
    book.add_order(test_helpers::make_order(
        "ASK",
        OrderSide::Sell,
        99_00,
        10,
        OrderType::Limit,
        TimeInForce::GTC,
    ));
    let (fills, _) = book.match_orders();
    assert_eq!(fills.len(), 1);
    assert_eq!(
        fills[0].trade_price, 10_100,
        "Aggressor sell executes at the resting bid price"
    );
}

#[test]
fn test_stop_limit_at_limit_crosses_immediately() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use test_helpers::*;

    let mut book = make_book();
    seed_book(&mut book);

    // StopLimit buy triggered: limit price = 101 (crosses resting ask at 101)
    let mut stop_limit = make_order(
        "SL-B1",
        OrderSide::Buy,
        10_100,
        50,
        OrderType::StopLimit,
        TimeInForce::Day,
    );
    stop_limit.stop_price = 10_100;

    let r = book.process_aggressive(stop_limit.clone());
    assert!(r.rest_on_book);

    let mut resting = stop_limit;
    resting.order_type = crate::order_book::order::OrderType::Limit;
    resting.quantity = r.unfilled_qty;
    book.add_order(resting);
    let (fills, _) = book.match_orders();

    assert_eq!(fills.len(), 1);
    assert_eq!(fills[0].trade_price, 10_100);
    assert_eq!(fills[0].trade_quantity, 50);
}

#[test]
fn test_drain_triggered_stops_buy() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use test_helpers::*;

    let mut stop_book = vec![
        {
            let mut o = make_order(
                "BS1",
                OrderSide::Buy,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 10_500;
            o
        },
        {
            let mut o = make_order(
                "BS2",
                OrderSide::Buy,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 11_000;
            o
        },
        {
            let mut o = make_order(
                "BS3",
                OrderSide::Buy,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 10_000;
            o
        },
    ];

    // last_trade = 107 — triggers BS1 (>=105) and BS2... wait, BS2 needs >=110, not triggered
    // and BS3 needs >=100, so triggered
    let triggered = OrderBook::drain_triggered_stops(&mut stop_book, 10_700);

    assert_eq!(
        triggered.len(),
        2,
        "BS1 (stop=105) and BS3 (stop=100) should trigger"
    );
    assert_eq!(stop_book.len(), 1, "BS2 (stop=110) should remain");
    assert_eq!(stop_book[0].order_id, "BS2");
    assert!(triggered.iter().any(|o| o.order_id == "BS1"));
    assert!(triggered.iter().any(|o| o.order_id == "BS3"));
}

#[test]
fn test_drain_triggered_stops_sell() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use test_helpers::*;

    let mut stop_book = vec![
        {
            let mut o = make_order(
                "SS1",
                OrderSide::Sell,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 95_00;
            o
        },
        {
            let mut o = make_order(
                "SS2",
                OrderSide::Sell,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 90_00;
            o
        },
        {
            let mut o = make_order(
                "SS3",
                OrderSide::Sell,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 10_000;
            o
        },
    ];

    // last_trade = 93 — triggers SS1 (<=95) and SS3 (<=100), not SS2 (<=90)
    let triggered = OrderBook::drain_triggered_stops(&mut stop_book, 93_00);

    assert_eq!(triggered.len(), 2);
    assert_eq!(stop_book.len(), 1);
    assert_eq!(stop_book[0].order_id, "SS2");
    assert!(triggered.iter().any(|o| o.order_id == "SS1"));
    assert!(triggered.iter().any(|o| o.order_id == "SS3"));
}

#[test]
fn test_drain_no_triggers_when_price_unchanged() {
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use test_helpers::*;

    let mut stop_book = vec![
        {
            let mut o = make_order(
                "BS1",
                OrderSide::Buy,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 11_000;
            o
        },
        {
            let mut o = make_order(
                "SS1",
                OrderSide::Sell,
                0,
                100,
                OrderType::Stop,
                TimeInForce::Day,
            );
            o.stop_price = 90_00;
            o
        },
    ];

    // last_trade = 100 — neither triggers (buy needs >=110, sell needs <=90)
    let triggered = OrderBook::drain_triggered_stops(&mut stop_book, 10_000);

    assert_eq!(triggered.len(), 0);
    assert_eq!(stop_book.len(), 2, "Both stops should remain");
}
// ── Critical-fix regression tests (ANALYSIS.md §2.7) ──────────────────────────

#[cfg(test)]
mod critical_tests {
    use super::test_helpers::*;
    use crate::order_book::order::{OrderSide, OrderType, TimeInForce};

    // #1 — P01: FIFO within a price level on the aggressive path.
    #[test]
    fn p01_time_priority_fifo_within_level() {
        let mut book = make_book();
        book.add_order(make_order(
            "S-FIRST",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "S-SECOND",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        let r = book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert_eq!(r.fills[0].sell_order_id, "S-FIRST");
    }

    // #1 — P02: FIFO within a price level on the passive match_orders path.
    #[test]
    fn p02_time_priority_passive_match() {
        let mut book = make_book();
        book.add_order(make_order(
            "B-FIRST",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "B-SECOND",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        let (fills, _) = book.match_orders();
        assert_eq!(fills[0].buy_order_id, "B-FIRST");
    }

    // #1 — a partially filled order keeps its place at the front of the queue.
    #[test]
    fn partial_fill_keeps_front_of_queue() {
        let mut book = make_book();
        book.add_order(make_order(
            "S-FIRST",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "S-SECOND",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        let r1 = book.process_aggressive(make_order(
            "B1",
            OrderSide::Buy,
            10_000,
            4,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert_eq!(r1.fills[0].sell_order_id, "S-FIRST");
        let r2 = book.process_aggressive(make_order(
            "B2",
            OrderSide::Buy,
            10_000,
            8,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert_eq!(r2.fills.len(), 2);
        assert_eq!(
            (
                r2.fills[0].sell_order_id.as_str(),
                r2.fills[0].trade_quantity
            ),
            ("S-FIRST", 6)
        );
        assert_eq!(
            (
                r2.fills[1].sell_order_id.as_str(),
                r2.fills[1].trade_quantity
            ),
            ("S-SECOND", 2)
        );
    }

    // #2 — P04: a fully filled resting order leaves order_lookup; cancel is rejected.
    #[test]
    fn p04_filled_order_removed_from_lookup() {
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert!(!book.order_lookup.contains_key("S"));
        assert!(book.cancel_order("S").is_none());
    }

    // #2 — P05: a partial fill keeps the order in order_lookup with its remaining quantity.
    #[test]
    fn p05_partial_fill_updates_lookup_quantity() {
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            4,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert_eq!(book.order_lookup["S"].quantity, 6);
    }

    // #2 — P11: cancelling a partially filled order reports the remaining quantity.
    #[test]
    fn p11_partial_fill_cancel_reports_remaining_qty() {
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            4,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        let md = book.cancel_order("S").unwrap();
        assert_eq!(md.changes[0].quantity, 6);
        assert!(book.order_lookup.is_empty());
    }

    // #2 — passive match_orders path keeps order_lookup in sync for both sides.
    #[test]
    fn passive_match_syncs_lookup() {
        let mut book = make_book();
        book.add_order(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            4,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.match_orders();
        assert!(!book.order_lookup.contains_key("S"));
        assert_eq!(book.order_lookup["B"].quantity, 6);
    }

    // #2 — order_lookup does not grow without bound: fill N orders, lookup returns to 0.
    #[test]
    fn lookup_does_not_grow_after_fills() {
        const N: usize = 10_000;
        let mut book = make_book();
        for i in 0..N {
            book.add_order(make_order(
                &format!("S{}", i),
                OrderSide::Sell,
                10_000 + (i % 7) as i64,
                5,
                OrderType::Limit,
                TimeInForce::GTC,
            ));
            if i % 2 == 0 {
                book.process_aggressive(make_order(
                    &format!("B{}", i),
                    OrderSide::Buy,
                    20_000,
                    5,
                    OrderType::Limit,
                    TimeInForce::IOC,
                ));
            } else {
                book.add_order(make_order(
                    &format!("B{}", i),
                    OrderSide::Buy,
                    20_000,
                    5,
                    OrderType::Limit,
                    TimeInForce::GTC,
                ));
                book.match_orders();
            }
        }
        assert!(book.buy_orders.is_empty() && book.sell_orders.is_empty());
        assert_eq!(book.order_lookup.len(), 0);
    }

    // #2 — replace of a filled order is rejected and never resurrects quantity.
    #[test]
    fn replace_filled_order_rejected() {
        use crate::order_book::order_book::{OrderReject, OrderReplace};
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::IOC,
        ));
        assert_eq!(
            book.replace_order(
                "S",
                "S2",
                OrderReplace {
                    new_price: 0,
                    new_total_qty: 0,
                    new_time_in_force: None,
                    client_order_id: "S2",
                    timestamp: 0
                },
                "T"
            )
            .unwrap_err(),
            OrderReject::UnknownOrder
        );
        assert!(book.order_lookup.is_empty() && book.sell_orders.is_empty());
    }

    // #2 — replace of a partially filled order keeps only the remaining quantity.
    #[test]
    fn replace_partial_never_resurrects_original_qty() {
        use crate::order_book::order_book::{OrderReject, OrderReplace};
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.process_aggressive(make_order(
            "B",
            OrderSide::Buy,
            10_000,
            4,
            OrderType::Limit,
            TimeInForce::IOC,
        ));

        // Price-only amend: remaining 6 carries over.
        let (r, _) = book
            .replace_order(
                "S",
                "S2",
                OrderReplace {
                    new_price: 10_100,
                    new_total_qty: 0,
                    new_time_in_force: None,
                    client_order_id: "S2",
                    timestamp: 0,
                },
                "T",
            )
            .unwrap();
        assert_eq!(r.quantity, 6);
        // Re-stating the original total (10) leaves 10 - 4 filled = 6 open.
        let (r, _) = book
            .replace_order(
                "S2",
                "S3",
                OrderReplace {
                    new_price: 0,
                    new_total_qty: 10,
                    new_time_in_force: None,
                    client_order_id: "S3",
                    timestamp: 0,
                },
                "T",
            )
            .unwrap();
        assert_eq!(r.quantity, 6);
        // A total at or below the filled quantity is rejected.
        assert_eq!(
            book.replace_order(
                "S3",
                "S4",
                OrderReplace {
                    new_price: 0,
                    new_total_qty: 4,
                    new_time_in_force: None,
                    client_order_id: "S4",
                    timestamp: 0
                },
                "T"
            )
            .unwrap_err(),
            OrderReject::QuantityNotAboveFilled
        );
        assert_eq!(book.order_lookup.len(), 1);
        assert_eq!(
            book.sell_orders[&10_100]
                .iter()
                .map(|o| o.quantity)
                .sum::<u32>(),
            6
        );
    }

    // #2 — cancel of an already cancelled order is rejected.
    #[test]
    fn cancel_twice_rejected() {
        let mut book = make_book();
        book.add_order(make_order(
            "S",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        assert!(book.cancel_order("S").is_some());
        assert!(book.cancel_order("S").is_none());
    }

    // #3 — only the owner may cancel; a foreign cancel leaves the order untouched.
    #[test]
    fn ownership_cancel_requires_owner() {
        use crate::order_book::order_book::OrderReject;
        let mut book = make_book();
        let mut o = make_order(
            "ORD-1",
            OrderSide::Sell,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        );
        o.trader_id = "VICTIM".to_string();
        book.add_order(o);

        assert_eq!(
            book.cancel_order_for("ORD-1", "ATTACKER").unwrap_err(),
            OrderReject::NotOwner
        );
        assert_eq!(
            book.cancel_order_for("ORD-X", "ATTACKER").unwrap_err(),
            OrderReject::UnknownOrder
        );
        // Same reject text either way, so a foreign order's existence is not revealed.
        assert_eq!(
            OrderReject::NotOwner.reason(),
            OrderReject::UnknownOrder.reason()
        );
        assert_eq!(book.order_lookup["ORD-1"].quantity, 10);
        assert_eq!(book.sell_orders[&10_000].len(), 1);

        assert!(book.cancel_order_for("ORD-1", "VICTIM").is_ok());
        assert!(book.order_lookup.is_empty() && book.sell_orders.is_empty());
    }

    // #3 — only the owner may cancel/replace.
    #[test]
    fn ownership_replace_requires_owner() {
        use crate::order_book::order_book::{OrderReject, OrderReplace};
        let mut book = make_book();
        let mut o = make_order(
            "ORD-1",
            OrderSide::Buy,
            10_000,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        );
        o.trader_id = "VICTIM".to_string();
        book.add_order(o);

        assert_eq!(
            book.replace_order(
                "ORD-1",
                "ORD-2",
                OrderReplace {
                    new_price: 1_00,
                    new_total_qty: 0,
                    new_time_in_force: None,
                    client_order_id: "ORD-2",
                    timestamp: 0
                },
                "ATTACKER"
            )
            .unwrap_err(),
            OrderReject::NotOwner
        );
        assert_eq!(book.buy_orders[&10_000][0].order_id, "ORD-1");

        let (r, _) = book
            .replace_order(
                "ORD-1",
                "ORD-2",
                OrderReplace {
                    new_price: 10_100,
                    new_total_qty: 0,
                    new_time_in_force: None,
                    client_order_id: "ORD-2",
                    timestamp: 0,
                },
                "VICTIM",
            )
            .unwrap();
        assert_eq!(
            (r.trader_id.as_str(), r.price, r.quantity),
            ("VICTIM", 10_100, 10)
        );
    }

    #[test]
    fn p08_fok_liquidity_sum_does_not_overflow() {
        let mut book = make_book();
        book.add_order(make_order(
            "A1",
            OrderSide::Sell,
            10_000,
            u32::MAX,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        book.add_order(make_order(
            "A2",
            OrderSide::Sell,
            10_100,
            10,
            OrderType::Limit,
            TimeInForce::GTC,
        ));
        let r = book.process_aggressive(make_order(
            "F1",
            OrderSide::Buy,
            10_100,
            20,
            OrderType::Limit,
            TimeInForce::FOK,
        ));
        assert_eq!(r.unfilled_qty, 0);
        assert_eq!(r.fills.iter().map(|f| f.trade_quantity).sum::<u32>(), 20);
    }

    // ── Reference model: price-time priority with a flat Vec ──────────────────

    #[derive(Clone)]
    struct RefOrder {
        id: String,
        side: OrderSide,
        price: i64,
        qty: u32,
        seq: u64,
    }

    #[derive(Default)]
    struct RefBook {
        orders: Vec<RefOrder>,
        seq: u64,
    }

    type RefFill = (String, String, i64, u32); // (buy_id, sell_id, price, qty)

    impl RefBook {
        /// Match an incoming order against the opposite side: best price first,
        /// then earliest arrival. `passive_rest` = true models Day/GTC limits
        /// (remainder rests). Trades always execute at the resting order's price.
        fn submit(
            &mut self,
            id: &str,
            side: OrderSide,
            price: i64,
            mut qty: u32,
            passive_rest: bool,
        ) -> Vec<RefFill> {
            let mut fills = Vec::new();
            while qty > 0 {
                let best = self
                    .orders
                    .iter()
                    .enumerate()
                    .filter(|(_, o)| {
                        o.side != side
                            && match side {
                                OrderSide::Buy => o.price <= price,
                                OrderSide::Sell => o.price >= price,
                            }
                    })
                    .min_by_key(|(_, o)| {
                        (
                            match side {
                                OrderSide::Buy => o.price,
                                OrderSide::Sell => -o.price,
                            },
                            o.seq,
                        )
                    })
                    .map(|(i, _)| i);
                let Some(i) = best else { break };
                let q = qty.min(self.orders[i].qty);
                let resting = self.orders[i].clone();
                let px = resting.price;
                fills.push(match side {
                    OrderSide::Buy => (id.to_string(), resting.id.clone(), px, q),
                    OrderSide::Sell => (resting.id.clone(), id.to_string(), px, q),
                });
                qty -= q;
                self.orders[i].qty -= q;
                if self.orders[i].qty == 0 {
                    self.orders.remove(i);
                }
            }
            if passive_rest && qty > 0 {
                self.seq += 1;
                self.orders.push(RefOrder {
                    id: id.to_string(),
                    side,
                    price,
                    qty,
                    seq: self.seq,
                });
            }
            fills
        }

        fn cancel(&mut self, id: &str) {
            self.orders.retain(|o| o.id != id);
        }
    }

    fn xorshift(state: &mut u64) -> u64 {
        let mut x = *state;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        *state = x;
        x
    }

    #[test]
    fn property_matches_reference_model() {
        for seed in 1..=300u64 {
            let mut rng = seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1;
            let mut book = make_book();
            let mut model = RefBook::default();
            let mut ids: Vec<String> = Vec::new();

            for step in 0..200 {
                let roll = xorshift(&mut rng) % 10;
                if roll < 2 && !ids.is_empty() {
                    let id = ids[(xorshift(&mut rng) as usize) % ids.len()].clone();
                    let _ = book.cancel_order(&id);
                    model.cancel(&id);
                    continue;
                }
                let id = format!("O{}-{}", seed, step);
                let side = if xorshift(&mut rng).is_multiple_of(2) {
                    OrderSide::Buy
                } else {
                    OrderSide::Sell
                };
                let price = 98_00 + (xorshift(&mut rng) % 5) as i64 * 1_00;
                let qty = 1 + (xorshift(&mut rng) % 20) as u32;
                let ioc = roll >= 8;

                let engine_fills: Vec<RefFill> = if ioc {
                    let r = book.process_aggressive(make_order(
                        &id,
                        side,
                        price,
                        qty,
                        OrderType::Limit,
                        TimeInForce::IOC,
                    ));
                    r.fills
                        .into_iter()
                        .map(|f| {
                            (
                                f.buy_order_id,
                                f.sell_order_id,
                                f.trade_price,
                                f.trade_quantity,
                            )
                        })
                        .collect()
                } else {
                    book.add_order(make_order(
                        &id,
                        side,
                        price,
                        qty,
                        OrderType::Limit,
                        TimeInForce::GTC,
                    ));
                    ids.push(id.clone());
                    let (fills, _) = book.match_orders();
                    fills
                        .into_iter()
                        .map(|f| {
                            (
                                f.buy_order_id,
                                f.sell_order_id,
                                f.trade_price,
                                f.trade_quantity,
                            )
                        })
                        .collect()
                };
                let model_fills = model.submit(&id, side, price, qty, !ioc);
                assert_eq!(
                    engine_fills, model_fills,
                    "seed {} step {} order {}",
                    seed, step, id
                );
            }
        }
    }
}
