// SPDX-License-Identifier: MIT

use crate::order_book::broadcaster::MarketDataBroadcaster;
use crate::order_book::market_data::{
    MarketDataIncrementalUpdate, MarketDataSnapshot, MarketOrderUpdate, MarketUpdateType,
};
use crate::order_book::order::Order;
use crate::order_book::order::OrderSide;
use crate::order_book::order::TimeInForce;
use std::collections::{BTreeMap, HashMap, VecDeque};
use std::sync::Arc;

#[derive(Debug)]
pub struct OrderBook {
    pub symbol: String,
    pub broadcaster: Arc<MarketDataBroadcaster>,
    pub buy_orders: BTreeMap<i64, VecDeque<Order>>,
    pub sell_orders: BTreeMap<i64, VecDeque<Order>>,
    /// Live (resting) orders only, keyed by order_id, with their remaining quantity.
    pub order_lookup: HashMap<String, Order>,
    /// Cumulative filled quantity of each live order in `order_lookup`.
    pub cum_filled: HashMap<String, u32>,
    /// Arrival sequence of each live order; the lower value is the resting side.
    pub arrival: HashMap<String, u64>,
    next_arrival: u64,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderReject {
    /// Unknown, already filled or already cancelled.
    UnknownOrder,
    DuplicateOrderId,
    /// Order exists but belongs to another trader.
    NotOwner,
    /// Requested total quantity does not exceed what has already been filled.
    QuantityNotAboveFilled,
}

impl OrderReject {
    pub fn reason(&self) -> &'static str {
        match self {
            // NotOwner shares UnknownOrder's text so rejects don't reveal other traders' orders.
            OrderReject::UnknownOrder | OrderReject::NotOwner => {
                "Unknown order (filled, cancelled or never existed)"
            }
            OrderReject::DuplicateOrderId => "Duplicate order id",
            OrderReject::QuantityNotAboveFilled => "Replace quantity not above filled quantity",
        }
    }
}

impl OrderBook {
    pub fn new(symbol: &str, broadcaster: Arc<MarketDataBroadcaster>) -> Self {
        Self {
            symbol: symbol.to_string(),
            broadcaster,
            buy_orders: BTreeMap::new(),
            sell_orders: BTreeMap::new(),
            order_lookup: HashMap::new(),
            cum_filled: HashMap::new(),
            arrival: HashMap::new(),
            next_arrival: 0,
        }
    }

    /// ✅ Notify clients when adding an order
    pub fn add_order(&mut self, order: Order) -> MarketDataIncrementalUpdate {
        let price_level = match order.side {
            OrderSide::Buy => self
                .buy_orders
                .entry(order.price)
                .or_insert_with(VecDeque::new),
            OrderSide::Sell => self
                .sell_orders
                .entry(order.price)
                .or_insert_with(VecDeque::new),
        };
        price_level.push_back(order.clone());
        self.order_lookup
            .insert(order.order_id.clone(), order.clone());
        self.next_arrival += 1;
        self.arrival
            .insert(order.order_id.clone(), self.next_arrival);

        // ✅ Return market update when order is added
        self.generate_incremental_update(
            MarketUpdateType::NewOrder,
            order.price,
            order.quantity,
            order.side,
        )
    }

    /// ✅ Notify clients when canceling an order
    pub fn cancel_order(&mut self, order_id: &str) -> Option<MarketDataIncrementalUpdate> {
        if let Some(order) = self.order_lookup.remove(order_id) {
            self.cum_filled.remove(order_id);
            self.arrival.remove(order_id);
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
            return Some(self.generate_incremental_update(
                MarketUpdateType::CancelOrder,
                order.price,
                order.quantity,
                order.side,
            ));
        }
        None
    }

    /// Verify that `owner` (the authenticated trader) owns live order `order_id`.
    pub fn check_owner(&self, order_id: &str, owner: &str) -> Result<(), OrderReject> {
        match self.order_lookup.get(order_id) {
            None => Err(OrderReject::UnknownOrder),
            Some(o) if o.trader_id != owner => Err(OrderReject::NotOwner),
            Some(_) => Ok(()),
        }
    }

    /// Cancel a live order on behalf of `owner`; rejects without side effects otherwise.
    pub fn cancel_order_for(
        &mut self,
        order_id: &str,
        owner: &str,
    ) -> Result<MarketDataIncrementalUpdate, OrderReject> {
        self.check_owner(order_id, owner)?;
        self.cancel_order(order_id).ok_or(OrderReject::UnknownOrder)
    }

    /// Record a fill against a live order: drop it from the lookup when fully
    /// filled, otherwise keep it with its remaining quantity.
    pub(crate) fn sync_fill(
        lookup: &mut HashMap<String, Order>,
        cum: &mut HashMap<String, u32>,
        arrival: &mut HashMap<String, u64>,
        order_id: &str,
        remaining: u32,
        trade_qty: u32,
    ) {
        if remaining == 0 {
            lookup.remove(order_id);
            cum.remove(order_id);
            arrival.remove(order_id);
        } else if let Some(o) = lookup.get_mut(order_id) {
            o.quantity = remaining;
            *cum.entry(order_id.to_string()).or_insert(0) += trade_qty;
        }
    }

    /// Cancel/replace a live order. `new_price` 0 keeps the price; `new_total_qty`
    /// 0 keeps the remaining quantity, otherwise it is the new total order
    /// quantity (FIX OrderQty) and the replacement rests with total - filled.
    /// The replacement is added to the book; the caller runs match_orders().
    pub fn replace_order(
        &mut self,
        orig_id: &str,
        new_id: &str,
        new_price: i64,
        new_total_qty: u32,
        new_time_in_force: Option<TimeInForce>,
        client_order_id: &str,
        owner: &str,
        timestamp: u64,
    ) -> Result<(Order, MarketDataIncrementalUpdate), OrderReject> {
        self.check_owner(orig_id, owner)?;
        let orig = self
            .order_lookup
            .get(orig_id)
            .cloned()
            .ok_or(OrderReject::UnknownOrder)?;
        if new_id != orig_id && self.order_lookup.contains_key(new_id) {
            return Err(OrderReject::DuplicateOrderId);
        }
        let filled = self.cum_filled.get(orig_id).copied().unwrap_or(0);
        let leaves = if new_total_qty == 0 {
            orig.quantity
        } else if new_total_qty > filled {
            new_total_qty - filled
        } else {
            return Err(OrderReject::QuantityNotAboveFilled);
        };

        self.cancel_order(orig_id);
        let replacement = Order {
            order_id: new_id.to_string(),
            price: if new_price > 0 { new_price } else { orig.price },
            quantity: leaves,
            time_in_force: new_time_in_force.unwrap_or(orig.time_in_force),
            timestamp,
            client_order_id: client_order_id.to_string(),
            ..orig
        };
        let inc = self.add_order(replacement.clone());
        if filled > 0 {
            self.cum_filled.insert(new_id.to_string(), filled);
        }
        Ok((replacement, inc))
    }

    /// ✅ Send full snapshot when a client subscribes
    pub fn send_snapshot(&self, client_id: u32) {
        let snapshot = self.get_market_snapshot();
        self.broadcaster.broadcast_snapshot(&snapshot);
    }

    pub fn generate_incremental_update(
        &self,
        update_type: MarketUpdateType,
        price: i64,
        quantity: u32,
        side: OrderSide,
    ) -> MarketDataIncrementalUpdate {
        MarketDataIncrementalUpdate {
            symbol: self.symbol.clone(),
            changes: vec![MarketOrderUpdate {
                update_type,
                price,
                quantity,
                side,
            }],
        }
    }
    pub fn get_market_snapshot(&self) -> MarketDataSnapshot {
        let bids = self
            .buy_orders
            .iter()
            .map(|(price, orders)| (*price, orders.iter().map(|o| o.quantity).sum()))
            .collect();

        let asks = self
            .sell_orders
            .iter()
            .map(|(price, orders)| (*price, orders.iter().map(|o| o.quantity).sum()))
            .collect();

        MarketDataSnapshot {
            symbol: self.symbol.clone(),
            bids,
            asks,
        }
    }
}
