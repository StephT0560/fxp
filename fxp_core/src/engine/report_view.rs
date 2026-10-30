// SPDX-License-Identifier: MIT
//! Read-only visible book view for offline replay reports, never called live.
use super::*;
use crate::reproducibility::json::Json;
impl<R: Clone, C: Clock, I: ExecIds> Engine<R, C, I> {
    pub(crate) fn report_ladder(&self, owner: Option<&str>) -> Json {
        let side = |levels: Vec<(&i64, &std::collections::VecDeque<Order>)>| {
            Json::Array(
                levels
                    .into_iter()
                    .map(|(price, orders)| {
                        let mut own = Vec::new();
                        let quantity: u64 = orders
                            .iter()
                            .map(|o| {
                                let visible = self.book.visible(o);
                                if owner.is_some_and(|s| s == o.trader_id) {
                                    own.push(Json::object([
                                        ("order_id", Json::string(&o.order_id)),
                                        ("displayed", Json::string(visible.to_string())),
                                        ("leaves", Json::string(o.quantity.to_string())),
                                    ]));
                                }
                                u64::from(visible)
                            })
                            .sum();
                        Json::object([
                            ("price", Json::string(price.to_string())),
                            ("quantity", Json::string(quantity.to_string())),
                            ("own_orders", Json::Array(own)),
                        ])
                    })
                    .collect(),
            )
        };
        Json::object([
            (
                "bids",
                side(self.book.buy_orders.iter().rev().take(10).collect()),
            ),
            (
                "asks",
                side(self.book.sell_orders.iter().take(10).collect()),
            ),
            ("phase", Json::string(format!("{:?}", self.phase))),
        ])
    }
}
