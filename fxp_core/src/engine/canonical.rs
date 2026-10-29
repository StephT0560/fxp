// SPDX-License-Identifier: MIT
use super::*;
use crate::reproducibility::{StateWriter, canonical_event};
use std::collections::BTreeMap;
fn tif(w: &mut StateWriter, t: TimeInForce) {
    w.u64(match t {
        TimeInForce::Day => 1,
        TimeInForce::GTC => 2,
        TimeInForce::IOC => 3,
        TimeInForce::FOK => 4,
        TimeInForce::AtOpen => 5,
        TimeInForce::AtClose => 6,
        TimeInForce::Gtd => 7,
    });
}
fn time(w: &mut StateWriter, t: Time) {
    w.i64(t.seconds);
    w.i64(i64::from(t.nanos));
}
fn order(w: &mut StateWriter, o: &Order) {
    for s in [&o.order_id, &o.trader_id, &o.symbol] {
        w.text(s);
    }
    w.i64(o.price);
    w.u64(u64::from(o.quantity));
    w.u64(u64::from(o.display_quantity));
    w.u64(match o.side {
        OrderSide::Buy => 1,
        OrderSide::Sell => 2,
    });
    w.u64(match o.order_type {
        OrderType::Market => 1,
        OrderType::Limit => 2,
        OrderType::Stop => 3,
        OrderType::StopLimit => 4,
    });
    tif(w, o.time_in_force);
    w.u64(o.timestamp);
    w.i64(o.stop_price);
    for s in [&o.expire_date, &o.account, &o.client_order_id] {
        w.text(s);
    }
    w.text(o.stp.mode.as_str());
    w.text(&o.stp.group);
}
fn orders(w: &mut StateWriter, orders: &HashMap<String, Order>) {
    w.u64(orders.len() as u64);
    for (id, o) in orders.iter().collect::<BTreeMap<_, _>>() {
        w.text(id);
        order(w, o);
    }
}
impl<R: Clone, C: Clock, I: ExecIds> Engine<R, C, I> {
    /// Canonical state excludes connection ownership and includes priority and
    /// reserve bookkeeping, stopped/auction orders and retained known IDs.
    pub fn canonical_state(&self) -> Vec<u8> {
        let mut w = StateWriter::default();
        w.text(crate::reproducibility::STATE_VERSION);
        w.text(&self.symbol);
        for levels in [&self.book.buy_orders, &self.book.sell_orders] {
            w.u64(levels.len() as u64);
            for (price, queue) in levels {
                w.i64(*price);
                w.u64(queue.len() as u64);
                for o in queue {
                    order(&mut w, o);
                }
            }
        }
        orders(&mut w, &self.book.order_lookup);
        for map in [&self.book.cum_filled, &self.book.displayed] {
            w.u64(map.len() as u64);
            for (id, n) in map.iter().collect::<BTreeMap<_, _>>() {
                w.text(id);
                w.u64(u64::from(*n));
            }
        }
        w.u64(self.book.arrival.len() as u64);
        for (id, n) in self.book.arrival.iter().collect::<BTreeMap<_, _>>() {
            w.text(id);
            w.u64(*n);
        }
        w.u64(self.book.next_arrival);
        w.u64(u64::from(self.book.last_added.is_some()));
        if let Some(id) = &self.book.last_added {
            w.text(id);
        }
        w.u64(self.stop_book.len() as u64);
        for o in self.stop_book.orders() {
            order(&mut w, o);
        }
        orders(&mut w, &self.call_markets);
        orders(&mut w, &self.held_close);
        w.u64(self.states.len() as u64);
        for (id, s) in self.states.iter().collect::<BTreeMap<_, _>>() {
            w.text(id);
            w.text(&s.sender);
            tif(&mut w, s.time_in_force);
            w.text(&s.expire_date);
            w.u64(s.arrival);
            w.i64(i64::from(s.side));
            for n in [s.order_qty, s.display_quantity, s.cum_qty] {
                w.u64(u64::from(n));
            }
            w.i128(s.notional);
            w.text(&s.account);
            w.text(&s.client_order_id);
        }
        w.u64(self.phase as u64);
        w.u64(u64::from(self.last_trade_price.is_some()));
        if let Some(p) = self.last_trade_price {
            w.i64(p);
        }
        w.u64(self.trade_seq);
        w.u64(self.next_arrival);
        time(&mut w, self.last_seen);
        w.u64(u64::from(self.call_until.is_some()));
        if let Some(t) = self.call_until {
            time(&mut w, t);
        }
        w.u64(u64::from(self.last_indicative.is_some()));
        if let Some(a) = self.last_indicative {
            w.i64(a.price);
            w.u64(a.volume);
            w.i128(a.imbalance);
        }
        w.text(&self.schedule.map(|s| s.to_string()).unwrap_or_default());
        w.text(self.stp_default.as_str());
        w.u64(u64::from(self.symbol_rules.price_scale));
        w.i64(self.symbol_rules.tick_size);
        for n in [
            self.symbol_rules.lot_size,
            self.symbol_rules.min_quantity,
            self.symbol_rules.max_quantity,
        ] {
            w.u64(u64::from(n));
        }
        // This revision supports no pegs. An explicit empty section reserves
        // their place; adding a peg implementation requires a state version bump.
        w.u64(0);
        w.u64(self.status_history.len() as u64);
        for (id, (sender, report)) in self.status_history.iter().collect::<BTreeMap<_, _>>() {
            w.text(id);
            w.text(sender);
            w.bytes(&canonical_event(&Output::ExecutionReport((), report.clone())).unwrap());
        }
        w.u64(self.terminal_history.len() as u64);
        for (a, b) in &self.terminal_history {
            w.text(a);
            w.text(b);
        }
        w.0
    }
}
