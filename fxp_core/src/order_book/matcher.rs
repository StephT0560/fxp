// SPDX-License-Identifier: MIT

use crate::order_book::execution_report::ExecutionReport;
use crate::order_book::market_data::{
    MarketDataIncrementalUpdate, MarketOrderUpdate, MarketUpdateType,
};
use crate::order_book::order::{Order, OrderSide, OrderType, StpMode, TimeInForce};
use crate::order_book::order_book::OrderBook;

// ── Self-trade prevention ─────────────────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StpAction {
    /// The order (or its unfilled remainder) is canceled.
    Canceled,
    /// The order's quantity is reduced by this much; it stays live.
    Restated(u32),
}

/// A self-match prevented instead of printed. `before_fill` is the index of
/// the first fill that happened after it, so reports can be interleaved in
/// the order the book changed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StpEvent {
    pub order_id: String,
    pub action: StpAction,
    pub mode: StpMode,
    pub before_fill: usize,
}

/// STP events and book incrementals (cancels / reductions) produced by the
/// last matching call; drained by the caller.
#[derive(Debug, Default)]
pub struct StpOutcome {
    pub events: Vec<StpEvent>,
    pub updates: Vec<MarketDataIncrementalUpdate>,
}

/// Whether `newer` arriving against `older` is a self-match to prevent.
fn self_match(newer: &Order, older: &Order) -> bool {
    newer.stp.mode != StpMode::None && newer.stp_key() == older.stp_key()
}

/// Progress of an aggressive order sweeping the book.
struct Sweep {
    remaining: u32,
    fills: Vec<ExecutionReport>,
    fill_updates: Vec<MarketDataIncrementalUpdate>,
    /// STP canceled the aggressor; stop matching.
    stopped: bool,
}

impl Sweep {
    fn new(order: &Order) -> Self {
        Self {
            remaining: order.quantity,
            fills: Vec::new(),
            fill_updates: Vec::new(),
            stopped: false,
        }
    }

    fn live(&self) -> bool {
        self.remaining > 0 && !self.stopped
    }

    fn finish(self) -> AggressiveResult {
        AggressiveResult {
            fills: self.fills,
            fill_updates: self.fill_updates,
            unfilled_qty: if self.stopped { 0 } else { self.remaining },
            rest_on_book: false,
            stp_canceled: self.stopped,
        }
    }
}

// ── Result of processing an aggressive order ──────────────────────────────────

pub struct AggressiveResult {
    pub fills: Vec<ExecutionReport>,
    pub fill_updates: Vec<MarketDataIncrementalUpdate>,
    pub unfilled_qty: u32,
    /// Whether the unfilled remainder should be added to the resting book.
    /// true  → Limit Day/GTC (rest unfilled qty)
    /// false → Market, IOC, FOK (cancel remainder)
    pub rest_on_book: bool,
    /// Self-trade prevention canceled the order; it neither rests nor gets
    /// a plain remainder cancel (its report comes from the STP event).
    pub stp_canceled: bool,
}

impl OrderBook {
    // ── Passive matching ──────────────────────────────────────────────────────
    // Called after add_order() to cross newly-resting limit orders.

    pub fn match_orders(&mut self) -> (Vec<ExecutionReport>, Vec<MarketDataIncrementalUpdate>) {
        let mut fills = Vec::new();
        let mut fill_updates = Vec::new();

        loop {
            let best_bid = self.buy_orders.keys().next_back().cloned();
            let best_ask = self.sell_orders.keys().next().cloned();

            match (best_bid, best_ask) {
                (Some(bid_price), Some(ask_price)) if bid_price >= ask_price => {
                    let bid_orders = self.buy_orders.get_mut(&bid_price).unwrap();
                    let mut bid = match bid_orders.pop_front() {
                        Some(o) => o,
                        None => break,
                    };
                    let ask_orders = self.sell_orders.get_mut(&ask_price).unwrap();
                    let mut ask = match ask_orders.pop_front() {
                        Some(o) => o,
                        None => {
                            self.buy_orders.get_mut(&bid_price).unwrap().push_front(bid);
                            break;
                        }
                    };

                    // Trades execute at the price of the order that was resting first.
                    let bid_seq = self.arrival.get(&bid.order_id).copied().unwrap_or(u64::MAX);
                    let ask_seq = self.arrival.get(&ask.order_id).copied().unwrap_or(u64::MAX);

                    let bid_newer = bid_seq > ask_seq;
                    let (newer, older) = if bid_newer {
                        (&bid, &ask)
                    } else {
                        (&ask, &bid)
                    };
                    if self_match(newer, older) {
                        let mode = newer.stp.mode;
                        let (newer, older) = if bid_newer {
                            (&mut bid, &mut ask)
                        } else {
                            (&mut ask, &mut bid)
                        };
                        let (newer_qty, older_qty) = match mode {
                            StpMode::CancelNewest => (0, older.quantity),
                            StpMode::CancelOldest => (newer.quantity, 0),
                            StpMode::CancelBoth => (0, 0),
                            _ => {
                                let q = newer.quantity.min(older.quantity);
                                (newer.quantity - q, older.quantity - q)
                            }
                        };
                        let before_fill = fills.len();
                        for (o, left) in [(&mut *older, older_qty), (&mut *newer, newer_qty)] {
                            let cut = o.quantity - left;
                            if cut == 0 {
                                continue;
                            }
                            let md = self.generate_incremental_update(
                                MarketUpdateType::CancelOrder,
                                o.price,
                                cut,
                                o.side,
                            );
                            self.stp.updates.push(md);
                            o.quantity = left;
                            Self::sync_fill(
                                &mut self.order_lookup,
                                &mut self.cum_filled,
                                &mut self.arrival,
                                &o.order_id,
                                left,
                                0,
                            );
                            self.stp.events.push(StpEvent {
                                order_id: o.order_id.clone(),
                                action: if left == 0 {
                                    StpAction::Canceled
                                } else {
                                    StpAction::Restated(cut)
                                },
                                mode,
                                before_fill,
                            });
                        }
                        for (levels, price, o) in [
                            (&mut self.buy_orders, bid_price, bid),
                            (&mut self.sell_orders, ask_price, ask),
                        ] {
                            let level = levels.get_mut(&price).expect("level popped from");
                            if o.quantity > 0 {
                                level.push_front(o);
                            } else if level.is_empty() {
                                levels.remove(&price);
                            }
                        }
                        continue;
                    }
                    let trade_price = if bid_seq < ask_seq {
                        bid_price
                    } else {
                        ask_price
                    };

                    let trade_qty = bid.quantity.min(ask.quantity);
                    bid.quantity -= trade_qty;
                    ask.quantity -= trade_qty;

                    let buy_done = bid.quantity == 0;
                    let sell_done = ask.quantity == 0;
                    Self::sync_fill(
                        &mut self.order_lookup,
                        &mut self.cum_filled,
                        &mut self.arrival,
                        &bid.order_id,
                        bid.quantity,
                        trade_qty,
                    );
                    Self::sync_fill(
                        &mut self.order_lookup,
                        &mut self.cum_filled,
                        &mut self.arrival,
                        &ask.order_id,
                        ask.quantity,
                        trade_qty,
                    );

                    fills.push(ExecutionReport {
                        buy_order_id: bid.order_id.clone(),
                        sell_order_id: ask.order_id.clone(),
                        trade_price,
                        trade_quantity: trade_qty,
                        buy_fully_filled: buy_done,
                        sell_fully_filled: sell_done,
                    });
                    fill_updates.push(MarketDataIncrementalUpdate {
                        symbol: self.symbol.clone(),
                        changes: vec![MarketOrderUpdate {
                            update_type: MarketUpdateType::ModifyOrder,
                            price: trade_price,
                            quantity: trade_qty,
                            side: if bid_seq < ask_seq {
                                OrderSide::Buy
                            } else {
                                OrderSide::Sell
                            },
                        }],
                    });

                    if buy_done {
                        if bid_orders.is_empty() {
                            self.buy_orders.remove(&bid_price);
                        }
                    } else {
                        bid_orders.push_front(bid);
                    }
                    if sell_done {
                        if ask_orders.is_empty() {
                            self.sell_orders.remove(&ask_price);
                        }
                    } else {
                        ask_orders.push_front(ask);
                    }
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
            (OrderType::Market, _) => self.execute_market(order),

            (OrderType::Limit, TimeInForce::IOC) => self.execute_ioc(order),

            (OrderType::Limit, TimeInForce::FOK) => self.execute_fok(order),

            (OrderType::Limit, TimeInForce::Day | TimeInForce::GTC | TimeInForce::Gtd) =>
            // Resting limit orders — caller calls add_order() + match_orders()
            {
                AggressiveResult {
                    fills: Vec::new(),
                    fill_updates: Vec::new(),
                    unfilled_qty: order.quantity,
                    rest_on_book: true,
                    stp_canceled: false,
                }
            }

            // AT_OPEN / AT_CLOSE: participate in auction phases.
            // Rest on book — auction session handling is a future milestone.
            (OrderType::Limit, TimeInForce::AtOpen | TimeInForce::AtClose) => AggressiveResult {
                fills: Vec::new(),
                fill_updates: Vec::new(),
                unfilled_qty: order.quantity,
                rest_on_book: true,
                stp_canceled: false,
            },

            // Stop: triggered externally when last_trade_price crosses stop_price.
            // When triggered, becomes a market order.
            // process_aggressive is only called AFTER the stop fires —
            // before that the order sits in the stop_book in server.rs.
            (OrderType::Stop, _) => self.execute_market(order),

            // StopLimit: triggered externally when last_trade_price crosses stop_price.
            // When triggered, becomes a limit order with its own TIF.
            (OrderType::StopLimit, _) => self.process_aggressive(Order {
                order_type: OrderType::Limit,
                ..order
            }),
        }
    }

    // ── Market order ──────────────────────────────────────────────────────────
    // Fill at any available price. Cancel unfilled remainder — never rests.

    fn execute_market(&mut self, order: Order) -> AggressiveResult {
        let mut sweep = Sweep::new(&order);
        while sweep.live() {
            let price = match order.side {
                OrderSide::Buy => self.sell_orders.keys().next().cloned(),
                OrderSide::Sell => self.buy_orders.keys().next_back().cloned(),
            };
            let Some(price) = price else { break };
            self.fill_level(&order, price, &mut sweep);
        }
        sweep.finish()
    }

    // ── IOC ───────────────────────────────────────────────────────────────────
    // Fill as much as possible at or better than limit price. Cancel remainder.

    fn execute_ioc(&mut self, order: Order) -> AggressiveResult {
        let mut sweep = Sweep::new(&order);
        while sweep.live() {
            let price = match order.side {
                OrderSide::Buy => self.sell_orders.keys().next().cloned(),
                OrderSide::Sell => self.buy_orders.keys().next_back().cloned(),
            };
            let Some(price) = price else { break };

            // Stop if best opposite price no longer crosses our limit
            let crosses = match order.side {
                OrderSide::Buy => price <= order.price,
                OrderSide::Sell => price >= order.price,
            };
            if !crosses {
                break;
            }
            self.fill_level(&order, price, &mut sweep);
        }
        sweep.finish()
    }

    // ── FOK ───────────────────────────────────────────────────────────────────
    // Fill entire quantity at limit price or cancel everything. With STP, a
    // self-match before the quantity is covered kills the whole order
    // without touching the book (no partial fills, no resting cancels).

    fn execute_fok(&mut self, order: Order) -> AggressiveResult {
        let crossing: Box<dyn Iterator<Item = &Order>> = match order.side {
            OrderSide::Buy => Box::new(
                self.sell_orders
                    .range(..=order.price)
                    .flat_map(|(_, v)| v.iter()),
            ),
            OrderSide::Sell => Box::new(
                self.buy_orders
                    .range(order.price..)
                    .rev()
                    .flat_map(|(_, v)| v.iter()),
            ),
        };
        let want = u64::from(order.quantity);
        let mut available = 0u64;
        let mut self_matched = false;
        for resting in crossing {
            if self_match(&order, resting) {
                self_matched = true;
                break;
            }
            available += u64::from(resting.quantity);
            if available >= want {
                break;
            }
        }

        if self_matched {
            self.stp.events.push(StpEvent {
                order_id: order.order_id.clone(),
                action: StpAction::Canceled,
                mode: order.stp.mode,
                before_fill: 0,
            });
            return AggressiveResult {
                fills: Vec::new(),
                fill_updates: Vec::new(),
                unfilled_qty: 0,
                rest_on_book: false,
                stp_canceled: true,
            };
        }
        if available < want {
            // Insufficient liquidity — kill the order
            return AggressiveResult {
                fills: Vec::new(),
                fill_updates: Vec::new(),
                unfilled_qty: order.quantity,
                rest_on_book: false,
                stp_canceled: false,
            };
        }

        // Enough liquidity — execute as IOC (will fully fill)
        let mut result = self.execute_ioc(order);
        result.rest_on_book = false;
        result
    }

    // ── fill_level: fill aggressor against one resting price level ────────────

    fn fill_level(&mut self, aggressor: &Order, price: i64, sweep: &mut Sweep) {
        let resting = match aggressor.side {
            OrderSide::Buy => self.sell_orders.get_mut(&price),
            OrderSide::Sell => self.buy_orders.get_mut(&price),
        };
        let Some(resting) = resting else { return };

        while sweep.live() && !resting.is_empty() {
            let top = resting.front_mut().unwrap();

            if self_match(aggressor, top) {
                let mode = aggressor.stp.mode;
                let before_fill = sweep.fills.len();
                let (aggr_left, top_left) = match mode {
                    StpMode::CancelNewest => (0, top.quantity),
                    StpMode::CancelOldest => (sweep.remaining, 0),
                    StpMode::CancelBoth => (0, 0),
                    _ => {
                        let q = sweep.remaining.min(top.quantity);
                        (sweep.remaining - q, top.quantity - q)
                    }
                };
                let cut = top.quantity - top_left;
                if cut > 0 {
                    self.stp.updates.push(MarketDataIncrementalUpdate {
                        symbol: self.symbol.clone(),
                        changes: vec![MarketOrderUpdate {
                            update_type: MarketUpdateType::CancelOrder,
                            price,
                            quantity: cut,
                            side: top.side,
                        }],
                    });
                    top.quantity = top_left;
                    Self::sync_fill(
                        &mut self.order_lookup,
                        &mut self.cum_filled,
                        &mut self.arrival,
                        &top.order_id,
                        top_left,
                        0,
                    );
                    self.stp.events.push(StpEvent {
                        order_id: top.order_id.clone(),
                        action: if top_left == 0 {
                            StpAction::Canceled
                        } else {
                            StpAction::Restated(cut)
                        },
                        mode,
                        before_fill,
                    });
                }
                let aggr_cut = sweep.remaining - aggr_left;
                if aggr_left == 0 {
                    sweep.stopped = true;
                    self.stp.events.push(StpEvent {
                        order_id: aggressor.order_id.clone(),
                        action: StpAction::Canceled,
                        mode,
                        before_fill,
                    });
                } else if aggr_cut > 0 {
                    sweep.remaining = aggr_left;
                    self.stp.events.push(StpEvent {
                        order_id: aggressor.order_id.clone(),
                        action: StpAction::Restated(aggr_cut),
                        mode,
                        before_fill,
                    });
                }
                if top_left == 0 {
                    resting.pop_front();
                }
                continue;
            }

            let trade_qty = top.quantity.min(sweep.remaining);
            top.quantity -= trade_qty;
            sweep.remaining -= trade_qty;

            let resting_done = top.quantity == 0;
            Self::sync_fill(
                &mut self.order_lookup,
                &mut self.cum_filled,
                &mut self.arrival,
                &top.order_id,
                top.quantity,
                trade_qty,
            );
            let aggressor_done = sweep.remaining == 0;

            let (buy_id, sell_id, buy_done, sell_done) = match aggressor.side {
                OrderSide::Buy => (
                    aggressor.order_id.clone(),
                    top.order_id.clone(),
                    aggressor_done,
                    resting_done,
                ),
                OrderSide::Sell => (
                    top.order_id.clone(),
                    aggressor.order_id.clone(),
                    resting_done,
                    aggressor_done,
                ),
            };

            sweep.fills.push(ExecutionReport {
                buy_order_id: buy_id,
                sell_order_id: sell_id,
                trade_price: price,
                trade_quantity: trade_qty,
                buy_fully_filled: buy_done,
                sell_fully_filled: sell_done,
            });
            sweep.fill_updates.push(MarketDataIncrementalUpdate {
                symbol: aggressor.symbol.clone(),
                changes: vec![MarketOrderUpdate {
                    update_type: MarketUpdateType::ModifyOrder,
                    price,
                    quantity: trade_qty,
                    side: match aggressor.side {
                        OrderSide::Buy => OrderSide::Sell,
                        OrderSide::Sell => OrderSide::Buy,
                    },
                }],
            });

            if resting_done {
                resting.pop_front();
            }
        }

        // Remove empty price level
        if resting.is_empty() {
            match aggressor.side {
                OrderSide::Buy => {
                    self.sell_orders.remove(&price);
                }
                OrderSide::Sell => {
                    self.buy_orders.remove(&price);
                }
            }
        }
    }

    // ── Stop order trigger check ──────────────────────────────────────────────
    // Called after every fill with the new last_trade_price.
    // Returns orders that have been triggered and should be processed.
    // The caller (order_book_task in server.rs) processes them and calls
    // check_stop_triggers again if those produce fills (cascade handling).

    pub fn drain_triggered_stops(stop_book: &mut Vec<Order>, last_trade_price: i64) -> Vec<Order> {
        let mut triggered = Vec::new();
        let mut remaining = Vec::new();

        for order in stop_book.drain(..) {
            let fires = match order.side {
                // Buy stop: fires when price rises to or above stop_price
                OrderSide::Buy => last_trade_price >= order.stop_price,
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
        // Fire in the order the price path reached them: farthest from the
        // print first (buy stops ascending, sell stops descending), ties by
        // arrival (stable sort over arrival-ordered input).
        triggered.sort_by_key(|o| std::cmp::Reverse((o.stop_price - last_trade_price).abs()));
        triggered
    }
}
