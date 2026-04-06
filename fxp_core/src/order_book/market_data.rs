#[derive(Debug)]
pub struct MarketDataSnapshot {
    pub symbol: String,
    pub bids: Vec<(i64, u32)>, // (price, quantity)
    pub asks: Vec<(i64, u32)>, // (price, quantity)
}

#[derive(Debug)]
pub struct MarketDataIncrementalUpdate {
    pub symbol: String,
    pub changes: Vec<MarketOrderUpdate>,
}

#[derive(Debug, PartialEq)]
pub enum MarketUpdateType {
    NewOrder,
    ModifyOrder,
    CancelOrder,
}

#[derive(Debug)]
pub struct MarketOrderUpdate {
    pub update_type: MarketUpdateType,
    pub price: i64,
    pub quantity: u32,
}
