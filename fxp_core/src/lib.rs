pub mod server;
pub mod order_book;

#[test]
fn test_full_order_lifecycle() {
    use crate::order_book::broadcaster::MarketDataBroadcaster;
    use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
    use crate::order_book::order_book::OrderBook;
    use std::sync::Arc;

    let broadcaster = Arc::new(MarketDataBroadcaster::new());
    let mut order_book = OrderBook::new("AAPL", broadcaster.clone());

    // ✅ Step 1: Client subscribes to market data
    broadcaster.subscribe(1, "AAPL");

    // ✅ Step 2: Trader A places a buy order
    let buy_order = Order {
        order_id: "B1".to_string(),
        trader_id: "TraderA".to_string(),
        symbol: "AAPL".to_string(),
        price: 101_00,
        quantity: 10,
        side: OrderSide::Buy,
        order_type: OrderType::Limit,
        time_in_force: TimeInForce::GTC,
        timestamp: 1700000000000,
    };
    let buy_update = order_book.add_order(buy_order.clone());
    broadcaster.broadcast_incremental(&buy_update);

    // ✅ Step 3: Trader B places a matching sell order
    let sell_order = Order {
        order_id: "S1".to_string(),
        trader_id: "TraderB".to_string(),
        symbol: "AAPL".to_string(),
        price: 101_00,
        quantity: 10,
        side: OrderSide::Sell,
        order_type: OrderType::Limit,
        time_in_force: TimeInForce::GTC,
        timestamp: 1700000000001,
    };
    let sell_update = order_book.add_order(sell_order.clone());
    broadcaster.broadcast_incremental(&sell_update);

    // ✅ Step 4: Match orders and execute trade
    let (execution_reports, market_updates) = order_book.match_orders();

    assert_eq!(execution_reports.len(), 1, "Expected exactly one fill");
    assert_eq!(execution_reports[0].trade_price, 101_00);
    assert_eq!(execution_reports[0].trade_quantity, 10);
    assert!(execution_reports[0].buy_fully_filled, "Buy should be fully filled");
    assert!(execution_reports[0].sell_fully_filled, "Sell should be fully filled");
    assert_eq!(execution_reports[0].buy_order_id, "B1");
    assert_eq!(execution_reports[0].sell_order_id, "S1");

    assert_eq!(market_updates.len(), 1, "Expected one fill market update");
    assert_eq!(market_updates[0].symbol, "AAPL");

    // ✅ Step 5: Broadcast market data updates after trade execution
    broadcaster.broadcast_snapshot(&order_book.get_market_snapshot());
}