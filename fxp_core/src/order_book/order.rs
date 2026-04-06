#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Order {
    pub order_id: String,
    pub trader_id: String,
    pub symbol: String,
    pub price: i64, // Fixed-point integer representation
    pub quantity: u32,
    pub side: OrderSide,
    pub order_type: OrderType,
    pub time_in_force: TimeInForce,
    pub timestamp: u64, // UNIX timestamp in milliseconds
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderSide {
    Buy,
    Sell,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderType {
    Market,
    Limit,
    Stop,
    StopLimit,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TimeInForce {
    Day,
    GTC, // Good-Til-Cancelled
    IOC, // Immediate or Cancel
    FOK, // Fill or Kill
}
