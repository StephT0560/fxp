// SPDX-License-Identifier: MIT

//! Parked stop orders and the per-print trigger cascade.
//!
//! Every print (fill) is checked against the parked stops in the order the
//! prints happened, so a stop crossed by an intermediate print of a sweep
//! fires even if the sweep ends on the other side of its stop price. Stops
//! triggered by one print fire in the order the price path reached them
//! (buy stops by ascending, sell stops by descending stop price), then by
//! arrival. Triggered stops execute FIFO; their own prints can trigger more.

use std::collections::VecDeque;

use crate::order_book::execution_report::ExecutionReport;
use crate::order_book::market_data::MarketDataIncrementalUpdate;
use crate::order_book::order::{Order, OrderSide, OrderType, TimeInForce};
use crate::order_book::order_book::{OrderBook, OrderReject};

/// Whether a print at `price` triggers the stop.
pub fn stop_fires(order: &Order, price: i64) -> bool {
    match order.side {
        OrderSide::Buy => price >= order.stop_price,
        OrderSide::Sell => price <= order.stop_price,
    }
}

/// Changes requested by a cancel/replace of a parked stop (0 / None = keep).
pub struct StopReplace<'a> {
    pub new_price: i64,
    pub new_stop_price: i64,
    pub new_quantity: u32,
    pub new_time_in_force: Option<TimeInForce>,
    pub client_order_id: &'a str,
    pub timestamp: u64,
}

/// Parked Stop / StopLimit orders, in arrival order.
#[derive(Default)]
pub struct StopBook {
    parked: Vec<Order>,
}

impl StopBook {
    pub fn park(&mut self, order: Order) {
        self.parked.push(order);
    }

    pub fn contains(&self, order_id: &str) -> bool {
        self.parked.iter().any(|o| o.order_id == order_id)
    }

    pub fn len(&self) -> usize {
        self.parked.len()
    }

    pub fn is_empty(&self) -> bool {
        self.parked.is_empty()
    }

    pub fn ids(&self) -> impl Iterator<Item = &str> {
        self.parked.iter().map(|o| o.order_id.as_str())
    }

    fn position_for(&self, order_id: &str, owner: &str) -> Result<usize, OrderReject> {
        match self.parked.iter().position(|o| o.order_id == order_id) {
            None => Err(OrderReject::UnknownOrder),
            Some(i) if self.parked[i].trader_id != owner => Err(OrderReject::NotOwner),
            Some(i) => Ok(i),
        }
    }

    /// Remove a parked stop on behalf of `owner`; rejects without side effects otherwise.
    pub fn cancel_for(&mut self, order_id: &str, owner: &str) -> Result<Order, OrderReject> {
        let i = self.position_for(order_id, owner)?;
        Ok(self.parked.remove(i))
    }

    /// Replace a parked stop. The replacement is re-parked at the back, so it
    /// loses time priority among stops with the same stop price.
    /// `id_in_use` reports whether `new_id` is live on the order book.
    pub fn replace_for(
        &mut self,
        orig_id: &str,
        new_id: &str,
        changes: StopReplace,
        owner: &str,
        id_in_use: bool,
    ) -> Result<Order, OrderReject> {
        let i = self.position_for(orig_id, owner)?;
        if new_id != orig_id && (id_in_use || self.contains(new_id)) {
            return Err(OrderReject::DuplicateOrderId);
        }
        let orig = self.parked.remove(i);
        let replacement = Order {
            order_id: new_id.to_string(),
            price: if changes.new_price > 0 {
                changes.new_price
            } else {
                orig.price
            },
            stop_price: if changes.new_stop_price > 0 {
                changes.new_stop_price
            } else {
                orig.stop_price
            },
            quantity: if changes.new_quantity > 0 {
                changes.new_quantity
            } else {
                orig.quantity
            },
            time_in_force: changes.new_time_in_force.unwrap_or(orig.time_in_force),
            timestamp: changes.timestamp,
            client_order_id: changes.client_order_id.to_string(),
            ..orig
        };
        self.parked.push(replacement.clone());
        Ok(replacement)
    }

    /// Remove and return the stops triggered by a print at `price`, in firing order.
    pub fn drain_triggered(&mut self, price: i64) -> Vec<Order> {
        OrderBook::drain_triggered_stops(&mut self.parked, price)
    }
}

/// Everything an incoming order (and any stops it triggers) did to the book.
#[derive(Default)]
pub struct Execution {
    /// Prints, in the order they happened.
    pub fills: Vec<ExecutionReport>,
    pub fill_updates: Vec<MarketDataIncrementalUpdate>,
    /// Incrementals for orders that came to rest on the book.
    pub rested: Vec<MarketDataIncrementalUpdate>,
    /// Orders whose unfilled remainder is cancelled (Market / IOC / FOK).
    pub cancels: Vec<String>,
    /// Stops triggered while processing, in firing order.
    pub triggered: Vec<String>,
}

impl OrderBook {
    /// Execute an order against the book, honouring its type and TIF. A
    /// triggered Stop executes as Market, a triggered StopLimit as a Limit
    /// with its own TIF. Returns whether a remainder rested on the book.
    pub fn execute_into(&mut self, order: Order, out: &mut Execution) -> bool {
        let order = Order {
            order_type: match order.order_type {
                OrderType::Stop => OrderType::Market,
                OrderType::StopLimit => OrderType::Limit,
                other => other,
            },
            ..order
        };
        let result = self.process_aggressive(order.clone());
        out.fills.extend(result.fills);
        out.fill_updates.extend(result.fill_updates);
        if result.unfilled_qty == 0 {
            return false;
        }
        if !result.rest_on_book {
            out.cancels.push(order.order_id);
            return false;
        }
        out.rested.push(self.add_order(Order {
            quantity: result.unfilled_qty,
            ..order
        }));
        let (fills, updates) = self.match_orders();
        out.fills.extend(fills);
        out.fill_updates.extend(updates);
        true
    }
}

/// Check every print in `out.fills` against the parked stops and execute
/// triggered stops until no new print triggers anything.
///
/// The incoming order finishes matching before any stop it triggers runs:
/// it arrived first, so a stop elected mid-sweep never takes liquidity
/// ahead of it. A stop's trigger is still decided by the print that
/// crossed it, not by the batch's last price.
pub fn run_stop_cascade(
    book: &mut OrderBook,
    stops: &mut StopBook,
    last_trade_price: &mut Option<i64>,
    out: &mut Execution,
) {
    let mut scanned = 0;
    let mut queue = VecDeque::new();
    loop {
        while scanned < out.fills.len() {
            let price = out.fills[scanned].trade_price;
            *last_trade_price = Some(price);
            queue.extend(stops.drain_triggered(price));
            scanned += 1;
        }
        let Some(stop) = queue.pop_front() else {
            break;
        };
        out.triggered.push(stop.order_id.clone());
        book.execute_into(stop, out);
    }
}

/// Seeded random add / cancel / replace / IOC / FOK / stop operations against
/// a naive reference book (linear scans, no indexes). Failures print the seed.
#[cfg(test)]
mod property_tests {
    use super::*;
    use crate::order_book::broadcaster::MarketDataBroadcaster;
    use std::sync::Arc;

    type Fill = (String, String, i64, u32);

    struct Rng(u64);
    impl Rng {
        fn next(&mut self) -> u64 {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            self.0
        }
        fn range(&mut self, lo: u64, hi: u64) -> u64 {
            lo + self.next() % (hi - lo + 1)
        }
    }

    #[derive(Default)]
    struct Reference {
        book: Vec<Order>, // arrival order
        stops: Vec<Order>,
        last: Option<i64>,
        fills: Vec<Fill>,
    }

    impl Reference {
        fn best(&self, o: &Order) -> Option<usize> {
            let mut best: Option<usize> = None;
            for (i, r) in self.book.iter().enumerate() {
                let crosses = r.side != o.side
                    && match (o.order_type, o.side) {
                        (OrderType::Market, _) => true,
                        (_, OrderSide::Buy) => r.price <= o.price,
                        (_, OrderSide::Sell) => r.price >= o.price,
                    };
                let better = |b: &Order| match o.side {
                    OrderSide::Buy => r.price < b.price,
                    OrderSide::Sell => r.price > b.price,
                };
                if crosses && best.is_none_or(|b| better(&self.book[b])) {
                    best = Some(i);
                }
            }
            best
        }

        fn execute(&mut self, mut o: Order) {
            o.order_type = match o.order_type {
                OrderType::Stop => OrderType::Market,
                OrderType::StopLimit => OrderType::Limit,
                t => t,
            };
            if o.order_type == OrderType::Limit && o.time_in_force == TimeInForce::FOK {
                let mut avail = 0u64;
                let mut probe = self.book.clone();
                while let Some(i) = self.best(&o) {
                    avail += u64::from(self.book[i].quantity);
                    self.book.remove(i);
                }
                self.book = std::mem::take(&mut probe);
                if avail < u64::from(o.quantity) {
                    return;
                }
            }
            while o.quantity > 0 {
                let Some(i) = self.best(&o) else { break };
                let r = &mut self.book[i];
                let q = r.quantity.min(o.quantity);
                let (b, s) = match o.side {
                    OrderSide::Buy => (o.order_id.clone(), r.order_id.clone()),
                    OrderSide::Sell => (r.order_id.clone(), o.order_id.clone()),
                };
                self.fills.push((b, s, r.price, q));
                r.quantity -= q;
                o.quantity -= q;
                if r.quantity == 0 {
                    self.book.remove(i);
                }
            }
            let rests = matches!(o.time_in_force, TimeInForce::Day | TimeInForce::GTC);
            if o.quantity > 0 && o.order_type == OrderType::Limit && rests {
                self.book.push(o);
            }
        }

        fn cascade(&mut self, mut scanned: usize) {
            let mut queue = VecDeque::new();
            loop {
                while scanned < self.fills.len() {
                    let p = self.fills[scanned].2;
                    self.last = Some(p);
                    let (mut hit, keep): (Vec<_>, Vec<_>) =
                        self.stops.drain(..).partition(|s| stop_fires(s, p));
                    self.stops = keep;
                    hit.sort_by_key(|s| std::cmp::Reverse((s.stop_price - p).abs()));
                    queue.extend(hit);
                    scanned += 1;
                }
                let Some(s) = queue.pop_front() else { break };
                self.execute(s);
            }
        }

        fn submit(&mut self, o: Order) {
            let is_stop = matches!(o.order_type, OrderType::Stop | OrderType::StopLimit);
            if is_stop && !self.last.is_some_and(|p| stop_fires(&o, p)) {
                self.stops.push(o);
                return;
            }
            let n = self.fills.len();
            self.execute(o);
            self.cascade(n);
        }

        fn cancel(&mut self, id: &str) {
            self.book.retain(|o| o.order_id != id);
            self.stops.retain(|o| o.order_id != id);
        }

        fn replace(&mut self, id: &str, new_id: &str, px: i64) {
            let n = self.fills.len();
            if let Some(i) = self.book.iter().position(|o| o.order_id == id) {
                let o = self.book.remove(i);
                self.execute(Order {
                    order_id: new_id.into(),
                    price: px,
                    ..o
                });
            } else if let Some(i) = self.stops.iter().position(|o| o.order_id == id) {
                let o = self.stops.remove(i);
                let o = Order {
                    order_id: new_id.into(),
                    stop_price: px,
                    ..o
                };
                if self.last.is_some_and(|p| stop_fires(&o, p)) {
                    self.execute(o);
                } else {
                    self.stops.push(o);
                }
            }
            self.cascade(n);
        }
    }

    struct Engine {
        book: OrderBook,
        stops: StopBook,
        last: Option<i64>,
        fills: Vec<Fill>,
    }

    impl Engine {
        fn record(&mut self, mut ex: Execution) {
            run_stop_cascade(&mut self.book, &mut self.stops, &mut self.last, &mut ex);
            self.fills.extend(ex.fills.into_iter().map(|f| {
                (
                    f.buy_order_id,
                    f.sell_order_id,
                    f.trade_price,
                    f.trade_quantity,
                )
            }));
        }

        fn submit(&mut self, o: Order) {
            let is_stop = matches!(o.order_type, OrderType::Stop | OrderType::StopLimit);
            if is_stop && !self.last.is_some_and(|p| stop_fires(&o, p)) {
                self.stops.park(o);
                return;
            }
            let mut ex = Execution::default();
            self.book.execute_into(o, &mut ex);
            self.record(ex);
        }

        fn cancel(&mut self, id: &str) {
            if self.book.cancel_order_for(id, "T").is_err() {
                let _ = self.stops.cancel_for(id, "T");
            }
        }

        fn replace(&mut self, id: &str, new_id: &str, px: i64) {
            let mut ex = Execution::default();
            if self.stops.contains(id) {
                let changes = StopReplace {
                    new_price: 0,
                    new_stop_price: px,
                    new_quantity: 0,
                    new_time_in_force: None,
                    client_order_id: "",
                    timestamp: 0,
                };
                let o = self
                    .stops
                    .replace_for(id, new_id, changes, "T", false)
                    .unwrap();
                if self.last.is_some_and(|p| stop_fires(&o, p)) {
                    let o = self.stops.cancel_for(new_id, "T").unwrap();
                    self.book.execute_into(o, &mut ex);
                }
            } else if self
                .book
                .replace_order(id, new_id, px, 0, None, "", "T", 0)
                .is_ok()
            {
                let (fills, fill_updates) = self.book.match_orders();
                ex.fills = fills;
                ex.fill_updates = fill_updates;
            }
            self.record(ex);
        }
    }

    fn random_order(rng: &mut Rng, id: String) -> Order {
        let side = if rng.next() % 2 == 0 {
            OrderSide::Buy
        } else {
            OrderSide::Sell
        };
        let price = rng.range(95, 105) as i64 * 100;
        let qty = rng.range(1, 10) as u32;
        let tifs = [
            TimeInForce::Day,
            TimeInForce::GTC,
            TimeInForce::IOC,
            TimeInForce::FOK,
        ];
        let (kind, tif, price, stop) = match rng.range(0, 6) {
            0..=2 => (OrderType::Limit, tifs[rng.range(0, 3) as usize], price, 0),
            3 => (OrderType::Market, TimeInForce::Day, 0, 0),
            4 => (OrderType::Stop, TimeInForce::Day, 0, price),
            _ => {
                let stop = rng.range(95, 105) as i64 * 100;
                (
                    OrderType::StopLimit,
                    tifs[rng.range(0, 3) as usize],
                    price,
                    stop,
                )
            }
        };
        Order {
            stop_price: stop,
            ..Order::simple(id, "T".into(), "P".into(), price, qty, side, kind, tif, 0)
        }
    }

    #[test]
    fn engine_matches_naive_reference() {
        for seed in 1..=300u64 {
            let mut rng = Rng(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1);
            let mut reference = Reference::default();
            let mut engine = Engine {
                book: OrderBook::new("P", Arc::new(MarketDataBroadcaster::new())),
                stops: StopBook::default(),
                last: None,
                fills: Vec::new(),
            };
            let mut ids: Vec<String> = Vec::new();
            for step in 0..150 {
                let new_id = format!("O{step}");
                match rng.range(0, 9) {
                    0..=6 => {
                        let o = random_order(&mut rng, new_id.clone());
                        reference.submit(o.clone());
                        engine.submit(o);
                        ids.push(new_id);
                    }
                    7 if !ids.is_empty() => {
                        let id = ids[rng.range(0, ids.len() as u64 - 1) as usize].clone();
                        reference.cancel(&id);
                        engine.cancel(&id);
                    }
                    8 | 9 if !ids.is_empty() => {
                        let id = ids[rng.range(0, ids.len() as u64 - 1) as usize].clone();
                        let px = rng.range(95, 105) as i64 * 100;
                        reference.replace(&id, &new_id, px);
                        engine.replace(&id, &new_id, px);
                        ids.push(new_id);
                    }
                    _ => {}
                }
                assert_eq!(
                    engine.fills, reference.fills,
                    "seed {seed} step {step}: fills"
                );
                assert_eq!(engine.last, reference.last, "seed {seed} step {step}: last");
                let mut got: Vec<_> = engine
                    .book
                    .order_lookup
                    .values()
                    .map(|o| (o.order_id.clone(), o.price, o.quantity))
                    .collect();
                let mut want: Vec<_> = reference
                    .book
                    .iter()
                    .map(|o| (o.order_id.clone(), o.price, o.quantity))
                    .collect();
                got.sort();
                want.sort();
                assert_eq!(got, want, "seed {seed} step {step}: resting book");
                let mut got: Vec<_> = engine.stops.ids().map(String::from).collect();
                let mut want: Vec<_> = reference.stops.iter().map(|o| o.order_id.clone()).collect();
                got.sort();
                want.sort();
                assert_eq!(got, want, "seed {seed} step {step}: parked stops");
            }
        }
    }
}
