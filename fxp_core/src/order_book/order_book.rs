use std::collections::{BTreeMap, HashMap};
use std::sync::Arc; 
use crate::order_book::order::Order; 
use crate::order_book::order::OrderSide;
use crate::order_book::market_data::{MarketDataSnapshot, MarketDataIncrementalUpdate, MarketOrderUpdate, MarketUpdateType}; 
use crate::order_book::broadcaster::MarketDataBroadcaster; 

#[derive(Debug)]
pub struct OrderBook {
    pub symbol: String,
    pub broadcaster: Arc<MarketDataBroadcaster>, 
    pub buy_orders: BTreeMap<i64, Vec<Order>>,
    pub sell_orders: BTreeMap<i64, Vec<Order>>,
    pub order_lookup: HashMap<String, Order>,
}

impl OrderBook {
    pub fn new(symbol: &str, broadcaster: Arc<MarketDataBroadcaster>) -> Self {
        Self {
            symbol: symbol.to_string(),
            broadcaster,
            buy_orders: BTreeMap::new(),
            sell_orders: BTreeMap::new(),
            order_lookup: HashMap::new(),
        }
    }

    /// ✅ Notify clients when adding an order
    pub fn add_order(&mut self, order: Order) -> MarketDataIncrementalUpdate {
        let price_level = match order.side {
            OrderSide::Buy => self.buy_orders.entry(order.price).or_insert_with(Vec::new),
            OrderSide::Sell => self.sell_orders.entry(order.price).or_insert_with(Vec::new),
        };
        price_level.push(order.clone());
        self.order_lookup.insert(order.order_id.clone(), order.clone());
    
        // ✅ Return market update when order is added
        self.generate_incremental_update(MarketUpdateType::NewOrder, order.price, order.quantity)
    }
    

    /// ✅ Notify clients when canceling an order
    pub fn cancel_order(&mut self, order_id: &str) -> Option<MarketDataIncrementalUpdate> {
        if let Some(order) = self.order_lookup.remove(order_id) {
            let price_level = match order.side {
                OrderSide::Buy => self.buy_orders.get_mut(&order.price),
                OrderSide::Sell => self.sell_orders.get_mut(&order.price),
            };
    
            if let Some(orders) = price_level {
                orders.retain(|o| o.order_id != order_id);
                if orders.is_empty() {
                    match order.side {
                        OrderSide::Buy => self.buy_orders.remove(&order.price),
                        OrderSide::Sell => self.sell_orders.remove(&order.price),
                    };
                }
            }
    
            // ✅ Return incremental update when order is canceled
            return Some(self.generate_incremental_update(MarketUpdateType::CancelOrder, order.price, order.quantity));
        }
        None
    }
    
    /// ✅ Send full snapshot when a client subscribes
    pub fn send_snapshot(&self, client_id: u32) {
        let snapshot = self.get_market_snapshot();
        self.broadcaster.broadcast_snapshot(&snapshot);
    }

    pub fn generate_incremental_update(&self, update_type: MarketUpdateType, price: i64, quantity: u32) -> MarketDataIncrementalUpdate {
        MarketDataIncrementalUpdate {
            symbol: self.symbol.clone(),
            changes: vec![MarketOrderUpdate {
                update_type,
                price,
                quantity,
            }],
        }
    }
    pub fn get_market_snapshot(&self) -> MarketDataSnapshot {
        let bids = self.buy_orders.iter()
            .map(|(price, orders)| (*price, orders.iter().map(|o| o.quantity).sum()))
            .collect();

        let asks = self.sell_orders.iter()
            .map(|(price, orders)| (*price, orders.iter().map(|o| o.quantity).sum()))
            .collect();

        MarketDataSnapshot {
            symbol: self.symbol.clone(),
            bids,
            asks,
        }
    }
}


