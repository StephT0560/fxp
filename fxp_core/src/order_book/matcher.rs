use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::execution_report::ExecutionReport;
use crate::order_book::market_data::{MarketDataIncrementalUpdate, MarketOrderUpdate, MarketUpdateType};
use crate::order_book::order_book::OrderBook;

// ── Result of processing an aggressive order ──────────────────────────────────

pub struct AggressiveResult {
    pub fills:        Vec<ExecutionReport>,
    pub fill_updates: Vec<MarketDataIncrementalUpdate>,
    pub unfilled_qty: u32,
    /// Whether the unfilled remainder should be added to the resting book.
    /// true  → Limit Day/GTC (rest unfilled qty)
    /// false → Market, IOC, FOK (cancel remainder)
    pub rest_on_book: bool,
}

impl OrderBook {
    // ── Passive matching ──────────────────────────────────────────────────────
    // Called after add_order() to cross newly-resting limit orders.

    pub fn match_orders(&mut self) -> (Vec<ExecutionReport>, Vec<MarketDataIncrementalUpdate>) {
        let mut fills        = Vec::new();
        let mut fill_updates = Vec::new();

        loop {
            let best_bid = self.buy_orders.keys().next_back().cloned();
            let best_ask = self.sell_orders.keys().next().cloned();

            match (best_bid, best_ask) {
                (Some(bid_price), Some(ask_price)) if bid_price >= ask_price => {
                    let bid_orders = self.buy_orders.get_mut(&bid_price).unwrap();
                    let mut bid = match bid_orders.pop() {
                        Some(o) => o,
                        None    => break,
                    };
                    let ask_orders = self.sell_orders.get_mut(&ask_price).unwrap();
                    let mut ask = match ask_orders.pop() {
                        Some(o) => o,
                        None    => {
                            self.buy_orders.get_mut(&bid_price).unwrap().push(bid);
                            break;
                        }
                    };

                    let trade_qty = bid.quantity.min(ask.quantity);
                    bid.quantity -= trade_qty;
                    ask.quantity -= trade_qty;

                    let buy_done  = bid.quantity == 0;
                    let sell_done = ask.quantity == 0;

                    fills.push(ExecutionReport {
                        buy_order_id:     bid.order_id.clone(),
                        sell_order_id:    ask.order_id.clone(),
                        trade_price:      ask_price,
                        trade_quantity:   trade_qty,
                        buy_fully_filled:  buy_done,
                        sell_fully_filled: sell_done,
                    });
                    fill_updates.push(MarketDataIncrementalUpdate {
                        symbol:  self.symbol.clone(),
                        changes: vec![MarketOrderUpdate {
                            update_type: MarketUpdateType::ModifyOrder,
                            price:       ask_price,
                            quantity:    trade_qty,
                        }],
                    });

                    if buy_done  { if bid_orders.is_empty() { self.buy_orders.remove(&bid_price);  } }
                    else         { bid_orders.push(bid); }
                    if sell_done { if ask_orders.is_empty() { self.sell_orders.remove(&ask_price); } }
                    else         { ask_orders.push(ask); }
                }
                _ => break,
            }
        }

        (fills, fill_updates)
    }

    // ── Aggressive order entry point ──────────────────────────────────────────
    // Called by the symbol task BEFORE add_order() for any order type.
    // Returns AggressiveResult which tells the caller whether to rest the order.

    pub fn process_aggressive(&mut self, order: Order) -> AggressiveResult {
        match (order.order_type, order.time_in_force) {
            (OrderType::Market, _) =>
                self.execute_market(order),

            (OrderType::Limit, TimeInForce::IOC) =>
                self.execute_ioc(order),

            (OrderType::Limit, TimeInForce::FOK) =>
                self.execute_fok(order),

            (OrderType::Limit, TimeInForce::Day | TimeInForce::GTC | TimeInForce::Gtd) =>
                // Resting limit orders — caller calls add_order() + match_orders()
                AggressiveResult {
                    fills: Vec::new(), fill_updates: Vec::new(),
                    unfilled_qty: order.quantity, rest_on_book: true,
                },

            // AT_OPEN / AT_CLOSE: participate in auction phases.
            // Rest on book — auction session handling is a future milestone.
            (OrderType::Limit, TimeInForce::AtOpen | TimeInForce::AtClose) =>
                AggressiveResult {
                    fills: Vec::new(), fill_updates: Vec::new(),
                    unfilled_qty: order.quantity, rest_on_book: true,
                },

            // Stop: triggered externally when last_trade_price crosses stop_price.
            // When triggered, becomes a market order.
            // process_aggressive is only called AFTER the stop fires —
            // before that the order sits in the stop_book in server.rs.
            (OrderType::Stop, _) =>
                self.execute_market(order),

            // StopLimit: triggered externally when last_trade_price crosses stop_price.
            // When triggered, becomes a resting limit order (Day/GTC behaviour).
            (OrderType::StopLimit, _) =>
                AggressiveResult {
                    fills: Vec::new(), fill_updates: Vec::new(),
                    unfilled_qty: order.quantity, rest_on_book: true,
                },
        }
    }

    // ── Market order ──────────────────────────────────────────────────────────
    // Fill at any available price. Cancel unfilled remainder — never rests.

    fn execute_market(&mut self, order: Order) -> AggressiveResult {
        let mut remaining    = order.quantity;
        let mut fills        = Vec::new();
        let mut fill_updates = Vec::new();

        while remaining > 0 {
            let price = match order.side {
                OrderSide::Buy  => self.sell_orders.keys().next().cloned(),
                OrderSide::Sell => self.buy_orders.keys().next_back().cloned(),
            };
            let price = match price { Some(p) => p, None => break };

            let filled = self.fill_level(&order, price, remaining, &mut fills, &mut fill_updates);
            remaining -= filled;
        }

        AggressiveResult { fills, fill_updates, unfilled_qty: remaining, rest_on_book: false }
    }

    // ── IOC ───────────────────────────────────────────────────────────────────
    // Fill as much as possible at or better than limit price. Cancel remainder.

    fn execute_ioc(&mut self, order: Order) -> AggressiveResult {
        let mut remaining    = order.quantity;
        let mut fills        = Vec::new();
        let mut fill_updates = Vec::new();

        while remaining > 0 {
            let price = match order.side {
                OrderSide::Buy  => self.sell_orders.keys().next().cloned(),
                OrderSide::Sell => self.buy_orders.keys().next_back().cloned(),
            };
            let price = match price { Some(p) => p, None => break };

            // Stop if best opposite price no longer crosses our limit
            let crosses = match order.side {
                OrderSide::Buy  => price <= order.price,
                OrderSide::Sell => price >= order.price,
            };
            if !crosses { break; }

            let filled = self.fill_level(&order, price, remaining, &mut fills, &mut fill_updates);
            remaining -= filled;
        }

        AggressiveResult { fills, fill_updates, unfilled_qty: remaining, rest_on_book: false }
    }

    // ── FOK ───────────────────────────────────────────────────────────────────
    // Fill entire quantity at limit price or cancel everything.

    fn execute_fok(&mut self, order: Order) -> AggressiveResult {
        let available: u32 = match order.side {
            OrderSide::Buy => self.sell_orders.iter()
                .filter(|&(&p, _)| p <= order.price)
                .flat_map(|(_, v)| v.iter().map(|o| o.quantity))
                .sum(),
            OrderSide::Sell => self.buy_orders.iter()
                .filter(|&(&p, _)| p >= order.price)
                .flat_map(|(_, v)| v.iter().map(|o| o.quantity))
                .sum(),
        };

        if available < order.quantity {
            // Insufficient liquidity — kill the order
            return AggressiveResult {
                fills: Vec::new(), fill_updates: Vec::new(),
                unfilled_qty: order.quantity, rest_on_book: false,
            };
        }

        // Enough liquidity — execute as IOC (will fully fill)
        let mut result = self.execute_ioc(order);
        result.rest_on_book = false;
        result
    }

    // ── fill_level: fill aggressor against one resting price level ────────────
    // Returns qty filled at this level.

    fn fill_level(
        &mut self,
        aggressor:    &Order,
        price:        i64,
        want:         u32,
        fills:        &mut Vec<ExecutionReport>,
        fill_updates: &mut Vec<MarketDataIncrementalUpdate>,
    ) -> u32 {
        let resting = match aggressor.side {
            OrderSide::Buy  => self.sell_orders.get_mut(&price),
            OrderSide::Sell => self.buy_orders.get_mut(&price),
        };
        let resting = match resting { Some(v) => v, None => return 0 };

        let mut filled = 0u32;

        while !resting.is_empty() && filled < want {
            let top = resting.last_mut().unwrap();
            let trade_qty = top.quantity.min(want - filled);
            top.quantity -= trade_qty;
            filled       += trade_qty;

            let resting_done   = top.quantity == 0;
            let aggressor_done = filled >= want;

            let (buy_id, sell_id, buy_done, sell_done) = match aggressor.side {
                OrderSide::Buy  => (aggressor.order_id.clone(), top.order_id.clone(), aggressor_done, resting_done),
                OrderSide::Sell => (top.order_id.clone(), aggressor.order_id.clone(), resting_done,   aggressor_done),
            };

            fills.push(ExecutionReport {
                buy_order_id:     buy_id,
                sell_order_id:    sell_id,
                trade_price:      price,
                trade_quantity:   trade_qty,
                buy_fully_filled:  buy_done,
                sell_fully_filled: sell_done,
            });
            fill_updates.push(MarketDataIncrementalUpdate {
                symbol:  aggressor.symbol.clone(),
                changes: vec![MarketOrderUpdate {
                    update_type: MarketUpdateType::ModifyOrder,
                    price,
                    quantity:    trade_qty,
                }],
            });

            if resting_done { resting.pop(); }
        }

        // Remove empty price level
        if resting.is_empty() {
            match aggressor.side {
                OrderSide::Buy  => { self.sell_orders.remove(&price); }
                OrderSide::Sell => { self.buy_orders.remove(&price); }
            }
        }

        filled
    }

    // ── Stop order trigger check ──────────────────────────────────────────────
    // Called after every fill with the new last_trade_price.
    // Returns orders that have been triggered and should be processed.
    // The caller (order_book_task in server.rs) processes them and calls
    // check_stop_triggers again if those produce fills (cascade handling).

    pub fn drain_triggered_stops(
        stop_book: &mut Vec<Order>,
        last_trade_price: i64,
    ) -> Vec<Order> {
        let mut triggered = Vec::new();
        let mut remaining = Vec::new();

        for order in stop_book.drain(..) {
            let fires = match order.side {
                // Buy stop: fires when price rises to or above stop_price
                OrderSide::Buy  => last_trade_price >= order.stop_price,
                // Sell stop: fires when price falls to or below stop_price
                OrderSide::Sell => last_trade_price <= order.stop_price,
            };

            if fires {
                triggered.push(order);
            } else {
                remaining.push(order);
            }
        }

        *stop_book = remaining;
        triggered
    }
}