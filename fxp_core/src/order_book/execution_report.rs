#[derive(Debug)]
pub struct ExecutionReport {
    pub buy_order_id: String,
    pub sell_order_id: String,
    pub trade_price: i64,
    pub trade_quantity: u32,
}

impl ExecutionReport {
    pub fn new(buy_id: String, sell_id: String, price: i64, quantity: u32) -> Self {
        ExecutionReport {
            buy_order_id: buy_id,
            sell_order_id: sell_id,
            trade_price: price,
            trade_quantity: quantity,
        }
    }
}
