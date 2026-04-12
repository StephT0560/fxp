use crate::order_book::execution_report::ExecutionReport;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketOrderUpdate, MarketUpdateType};
use crate::order_book::order_book::OrderBook;

impl OrderBook {
    /// Match resting orders and return execution reports.
    ///
    /// Each report carries:
    ///   - `buy_order_id` / `sell_order_id` — both sides of the trade
    ///   - `trade_price`  — the resting ask price (price-time priority)
    ///   - `trade_quantity` — qty filled in this match
    ///   - `buy_fully_filled` / `sell_fully_filled` — lets the gateway know
    ///     whether to keep or remove the client mapping for each side
    ///
    /// Also returns incremental market data updates for every fill so the
    /// caller can broadcast them — previously these were built but discarded.
    pub fn match_orders(&mut self) -> (Vec<ExecutionReport>, Vec<MarketDataIncrementalUpdate>) {
        let mut execution_reports = Vec::new();
        let mut market_updates = Vec::new();

        loop {
            // Best bid = highest buy price (last key in ascending BTreeMap)
            // Best ask = lowest sell price (first key)
            let best_bid = self.buy_orders.keys().next_back().cloned();
            let best_ask = self.sell_orders.keys().next().cloned();

            match (best_bid, best_ask) {
                (Some(bid_price), Some(ask_price)) if bid_price >= ask_price => {
                    let bid_orders = self.buy_orders.get_mut(&bid_price).unwrap();
                    let mut bid_order = match bid_orders.pop() {
                        Some(o) => o,
                        None => break,
                    };

                    let ask_orders = self.sell_orders.get_mut(&ask_price).unwrap();
                    let mut ask_order = match ask_orders.pop() {
                        Some(o) => o,
                        None => {
                            // Put the bid back and break — ask side is empty at this price
                            self.buy_orders
                                .get_mut(&bid_price)
                                .unwrap()
                                .push(bid_order);
                            break;
                        }
                    };

                    let trade_qty = bid_order.quantity.min(ask_order.quantity);
                    bid_order.quantity -= trade_qty;
                    ask_order.quantity -= trade_qty;

                    let buy_fully_filled = bid_order.quantity == 0;
                    let sell_fully_filled = ask_order.quantity == 0;

                    execution_reports.push(ExecutionReport {
                        buy_order_id: bid_order.order_id.clone(),
                        sell_order_id: ask_order.order_id.clone(),
                        trade_price: ask_price,
                        trade_quantity: trade_qty,
                        buy_fully_filled,
                        sell_fully_filled,
                    });

                    // Market data update for this fill
                    market_updates.push(MarketDataIncrementalUpdate {
                        symbol: self.symbol.clone(),
                        changes: vec![MarketOrderUpdate {
                            update_type: MarketUpdateType::ModifyOrder,
                            price: ask_price,
                            quantity: trade_qty,
                        }],
                    });

                    // Return partially filled orders to the book,
                    // remove fully filled price levels
                    if buy_fully_filled {
                        if bid_orders.is_empty() {
                            self.buy_orders.remove(&bid_price);
                        }
                    } else {
                        bid_orders.push(bid_order);
                    }

                    if sell_fully_filled {
                        if ask_orders.is_empty() {
                            self.sell_orders.remove(&ask_price);
                        }
                    } else {
                        ask_orders.push(ask_order);
                    }
                }
                _ => break,
            }
        }

        (execution_reports, market_updates)
    }
}