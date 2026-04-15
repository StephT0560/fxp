#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Order {
    pub order_id:      String,
    pub trader_id:     String,
    pub symbol:        String,
    pub price:         i64,   // fixed-point, cents (e.g. 10050 = $100.50)
    pub quantity:      u32,
    pub side:          OrderSide,
    pub order_type:    OrderType,
    pub time_in_force: TimeInForce,
    pub timestamp:     u64,   // UNIX milliseconds
    // Extended fields from proto v1.1
    pub stop_price:    i64,   // required for Stop/StopLimit (0 if not applicable)
    pub expire_date:   String, // required for GTD (YYYY-MM-DD, empty if not applicable)
    pub account:       String,
    pub client_order_id: String,
}

impl Order {
    /// Convenience constructor for simple orders (tests and internal use).
    /// Extended fields default to zero/empty.
    pub fn simple(
        order_id: String, trader_id: String, symbol: String,
        price: i64, quantity: u32, side: OrderSide,
        order_type: OrderType, time_in_force: TimeInForce,
        timestamp: u64,
    ) -> Self {
        Self {
            order_id, trader_id, symbol, price, quantity,
            side, order_type, time_in_force, timestamp,
            stop_price: 0, expire_date: String::new(),
            account: String::new(), client_order_id: String::new(),
        }
    }
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
    Stop,      // triggers market order when stop_price is reached
    StopLimit, // triggers limit order when stop_price is reached
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TimeInForce {
    Day,     // expires at end of regular trading session
    GTC,     // Good Till Cancelled
    IOC,     // Immediate or Cancel
    FOK,     // Fill or Kill
    AtOpen,  // Opening auction participation only
    AtClose, // Closing auction participation only (MOC)
    Gtd,     // Good Till Date (see Order.expire_date)
}