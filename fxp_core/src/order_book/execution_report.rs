#[derive(Debug)]
pub struct ExecutionReport {
    pub buy_order_id: String,
    pub sell_order_id: String,
    pub trade_price: i64,
    pub trade_quantity: u32,
    /// True when the buy order is fully filled in this match.
    /// False means it was partially filled and remains in the book.
    pub buy_fully_filled: bool,
    /// True when the sell order is fully filled in this match.
    pub sell_fully_filled: bool,
}

impl ExecutionReport {
    pub fn new(
        buy_id: String,
        sell_id: String,
        price: i64,
        quantity: u32,
        buy_fully_filled: bool,
        sell_fully_filled: bool,
    ) -> Self {
        ExecutionReport {
            buy_order_id: buy_id,
            sell_order_id: sell_id,
            trade_price: price,
            trade_quantity: quantity,
            buy_fully_filled,
            sell_fully_filled,
        }
    }
}