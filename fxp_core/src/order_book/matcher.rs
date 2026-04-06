use crate::order_book::order::{Order, OrderSide};
use crate::order_book::execution_report::ExecutionReport;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketOrderUpdate, MarketUpdateType};
use crate::order_book::order_book::OrderBook;

impl OrderBook {
    pub fn match_orders(&mut self) -> Vec<ExecutionReport> {
        let mut execution_reports = Vec::new();
        let mut market_updates = Vec::new();

        loop {
            let best_bid = self.buy_orders.keys().next_back().cloned();
            let best_ask = self.sell_orders.keys().next().cloned();

            if let (Some(best_bid), Some(best_ask)) = (best_bid, best_ask) {
                if best_bid >= best_ask {
                    let bid_orders = self.buy_orders.get_mut(&best_bid).unwrap();
                    let ask_orders = self.sell_orders.get_mut(&best_ask).unwrap();

                    if let (Some(mut bid_order), Some(mut ask_order)) = (bid_orders.pop(), ask_orders.pop()) {
                        let trade_qty = bid_order.quantity.min(ask_order.quantity);
                        bid_order.quantity -= trade_qty;
                        ask_order.quantity -= trade_qty;

                        execution_reports.push(ExecutionReport::new(
                            bid_order.order_id.clone(),
                            ask_order.order_id.clone(),
                            best_ask,
                            trade_qty,
                        ));

                        market_updates.push(MarketDataIncrementalUpdate {
                            symbol: self.symbol.clone(),
                            changes: vec![MarketOrderUpdate {
                                update_type: MarketUpdateType::ModifyOrder,
                                price: best_ask,
                                quantity: trade_qty,
                            }],
                        });

                        if bid_order.quantity > 0 {
                            bid_orders.push(bid_order);
                        } else {
                            self.buy_orders.remove(&best_bid);
                        }

                        if ask_order.quantity > 0 {
                            ask_orders.push(ask_order);
                        } else {
                            self.sell_orders.remove(&best_ask);
                        }
                    }
                } else {
                    break;
                }
            } else {
                break;
            }
        }

        execution_reports
    }
}
