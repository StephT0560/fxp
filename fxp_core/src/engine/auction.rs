// SPDX-License-Identifier: MIT

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AuctionSide {
    Buy,
    Sell,
}

#[derive(Clone, Copy, Debug)]
pub struct AuctionOrder<'a> {
    pub id: &'a str,
    pub side: AuctionSide,
    pub limit_price: Option<i64>,
    pub quantity: u32,
    pub seq: u64,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AuctionResult {
    pub price: i64,
    pub volume: u64,
    pub imbalance: i128,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AuctionFill<'a> {
    pub buy_id: &'a str,
    pub sell_id: &'a str,
    pub quantity: u32,
    pub price: i64,
}

/// Clearing and indicative values; a zero-volume result has price zero and net total imbalance.
pub fn uncross(orders: &[AuctionOrder<'_>], reference_price: Option<i64>) -> AuctionResult {
    let mut levels = std::collections::BTreeMap::<i64, (u64, u64)>::new();
    let (mut buys, mut sells, mut total_sells) = (0_u64, 0_u64, 0_u64);
    for order in orders {
        let qty = u64::from(order.quantity);
        match order.side {
            AuctionSide::Buy => buys += qty,
            AuctionSide::Sell => {
                total_sells += qty;
                if order.limit_price.is_none() {
                    sells += qty;
                }
            }
        }
        if let Some(price) = order.limit_price {
            let level = levels.entry(price).or_default();
            match order.side {
                AuctionSide::Buy => level.0 += qty,
                AuctionSide::Sell => level.1 += qty,
            }
        }
    }
    let no_cross = AuctionResult {
        price: 0,
        volume: 0,
        imbalance: i128::from(buys) - i128::from(total_sells),
    };
    if levels.is_empty() {
        return match reference_price {
            Some(price) if buys.min(sells) > 0 => AuctionResult {
                price,
                volume: buys.min(sells),
                imbalance: no_cross.imbalance,
            },
            _ => no_cross,
        };
    }
    let mut candidates = Vec::with_capacity(levels.len());
    for (price, (at_buy, at_sell)) in levels {
        sells += at_sell;
        candidates.push(AuctionResult {
            price,
            volume: buys.min(sells),
            imbalance: i128::from(buys) - i128::from(sells),
        });
        buys -= at_buy;
    }
    let max_volume = candidates.iter().map(|r| r.volume).max().unwrap();
    if max_volume == 0 {
        return no_cross;
    }
    candidates.retain(|r| r.volume == max_volume);
    let min_imbalance = candidates.iter().map(|r| r.imbalance.abs()).min().unwrap();
    candidates.retain(|r| r.imbalance.abs() == min_imbalance);
    if let Some(reference) = reference_price {
        let distance = |r: &AuctionResult| (i128::from(r.price) - i128::from(reference)).abs();
        let closest = candidates.iter().map(distance).min().unwrap();
        candidates.retain(|r| distance(r) == closest);
    }
    if candidates.iter().all(|r| r.imbalance > 0) {
        *candidates.last().unwrap()
    } else {
        candidates[0]
    }
}

/// Allocates an uncross result for the same orders in market/price/time priority.
pub fn allocate<'a>(orders: &[AuctionOrder<'a>], result: AuctionResult) -> Vec<AuctionFill<'a>> {
    let queue = |side| {
        let mut eligible: Vec<_> = orders
            .iter()
            .filter(|o| {
                o.side == side
                    && o.quantity > 0
                    && o.limit_price.is_none_or(|p| match side {
                        AuctionSide::Buy => p >= result.price,
                        AuctionSide::Sell => p <= result.price,
                    })
            })
            .collect();
        eligible.sort_by(|a, b| {
            a.limit_price
                .is_some()
                .cmp(&b.limit_price.is_some())
                .then_with(|| match side {
                    AuctionSide::Buy => b.limit_price.cmp(&a.limit_price),
                    AuctionSide::Sell => a.limit_price.cmp(&b.limit_price),
                })
                .then(a.seq.cmp(&b.seq))
        });
        eligible
    };
    let buys = queue(AuctionSide::Buy);
    let sells = queue(AuctionSide::Sell);
    let (mut b, mut s, mut buy_used, mut sell_used) = (0, 0, 0, 0);
    let mut remaining = result.volume;
    let mut fills = Vec::new();
    while remaining > 0 && b < buys.len() && s < sells.len() {
        let buy_left = buys[b].quantity - buy_used;
        let sell_left = sells[s].quantity - sell_used;
        let quantity = u64::from(buy_left.min(sell_left)).min(remaining) as u32;
        fills.push(AuctionFill {
            buy_id: buys[b].id,
            sell_id: sells[s].id,
            quantity,
            price: result.price,
        });
        remaining -= u64::from(quantity);
        buy_used += quantity;
        sell_used += quantity;
        if buy_used == buys[b].quantity {
            b += 1;
            buy_used = 0;
        }
        if sell_used == sells[s].quantity {
            s += 1;
            sell_used = 0;
        }
    }
    fills
}

#[cfg(test)]
mod tests {
    use super::*;
    use AuctionSide::{Buy, Sell};
    use std::collections::HashMap;

    fn order(
        id: &str,
        side: AuctionSide,
        price: Option<i64>,
        quantity: u32,
        seq: u64,
    ) -> AuctionOrder<'_> {
        AuctionOrder {
            id,
            side,
            limit_price: price,
            quantity,
            seq,
        }
    }

    fn result(price: i64, volume: u64, imbalance: i128) -> AuctionResult {
        AuctionResult {
            price,
            volume,
            imbalance,
        }
    }

    #[test]
    fn simple_cross() {
        let orders = [
            order("B", Buy, Some(100), 5, 1),
            order("S", Sell, Some(90), 5, 2),
        ];
        let r = uncross(&orders, None);
        assert_eq!(r, result(90, 5, 0));
        assert_eq!(
            allocate(&orders, r),
            [AuctionFill {
                buy_id: "B",
                sell_id: "S",
                quantity: 5,
                price: 90
            }]
        );
    }

    #[test]
    fn volume_tie_minimizes_imbalance_before_reference() {
        let orders = [
            order("B1", Buy, Some(110), 10, 1),
            order("B2", Buy, Some(100), 10, 2),
            order("S", Sell, Some(90), 5, 3),
        ];
        assert_eq!(uncross(&orders, Some(90)), result(110, 5, 5));
    }

    #[test]
    fn reference_precedes_pressure() {
        let orders = [
            order("B", Buy, Some(110), 10, 1),
            order("S", Sell, Some(90), 5, 2),
        ];
        assert_eq!(uncross(&orders, Some(91)), result(90, 5, 5));
        let balanced = [
            order("B", Buy, Some(110), 5, 1),
            order("S", Sell, Some(90), 5, 2),
        ];
        assert_eq!(uncross(&balanced, Some(105)), result(110, 5, 0));
    }

    #[test]
    fn buy_and_sell_pressure() {
        let buy = [
            order("B", Buy, Some(110), 10, 1),
            order("S", Sell, Some(90), 5, 2),
        ];
        assert_eq!(uncross(&buy, None), result(110, 5, 5));
        assert_eq!(uncross(&buy, Some(100)), result(110, 5, 5));
        let sell = [
            order("B", Buy, Some(110), 5, 1),
            order("S", Sell, Some(90), 10, 2),
        ];
        assert_eq!(uncross(&sell, None), result(90, 5, -5));
    }

    #[test]
    fn mixed_or_balanced_pressure_falls_back_to_lowest() {
        let mixed = [
            order("B1", Buy, Some(110), 5, 1),
            order("B2", Buy, Some(90), 5, 2),
            order("S1", Sell, Some(90), 5, 3),
            order("S2", Sell, Some(110), 5, 4),
        ];
        assert_eq!(uncross(&mixed, None), result(90, 5, 5));
        let balanced = [
            order("B", Buy, Some(110), 5, 1),
            order("S", Sell, Some(90), 5, 2),
        ];
        assert_eq!(uncross(&balanced, Some(100)), result(90, 5, 0));
    }

    #[test]
    fn no_cross_one_side_and_empty() {
        let no_cross = [
            order("B", Buy, Some(90), 5, 1),
            order("S", Sell, Some(100), 3, 2),
        ];
        assert_eq!(uncross(&no_cross, Some(95)), result(0, 0, 2));
        assert!(allocate(&no_cross, uncross(&no_cross, None)).is_empty());
        assert_eq!(uncross(&no_cross[..1], None), result(0, 0, 5));
        assert_eq!(uncross(&no_cross[1..], None), result(0, 0, -3));
        assert_eq!(uncross(&[], Some(100)), result(0, 0, 0));
    }

    #[test]
    fn markets_only_need_reference() {
        let orders = [order("B", Buy, None, 5, 1), order("S", Sell, None, 3, 2)];
        assert_eq!(uncross(&orders, Some(100)), result(100, 3, 2));
        assert_eq!(uncross(&orders, None), result(0, 0, 2));
        assert_eq!(uncross(&orders[..1], Some(100)), result(0, 0, 5));
        assert_eq!(
            allocate(&orders, uncross(&orders, Some(100)))[0].quantity,
            3
        );
    }

    #[test]
    fn markets_plus_limits() {
        let orders = [
            order("BM", Buy, None, 3, 5),
            order("B", Buy, Some(100), 4, 1),
            order("SM", Sell, None, 2, 6),
            order("S", Sell, Some(90), 3, 2),
            order("S2", Sell, Some(110), 8, 3),
        ];
        let r = uncross(&orders, None);
        assert_eq!(r, result(100, 5, 2));
        assert_eq!(
            allocate(&orders, r),
            [
                AuctionFill {
                    buy_id: "BM",
                    sell_id: "SM",
                    quantity: 2,
                    price: 100
                },
                AuctionFill {
                    buy_id: "BM",
                    sell_id: "S",
                    quantity: 1,
                    price: 100
                },
                AuctionFill {
                    buy_id: "B",
                    sell_id: "S",
                    quantity: 2,
                    price: 100
                },
            ]
        );
    }

    #[test]
    fn clearing_level_allocation_is_price_time_priority_on_both_sides() {
        let buys = [
            order("new", Buy, Some(100), 3, 2),
            order("old", Buy, Some(100), 4, 1),
            order("better", Buy, Some(110), 2, 3),
            order("market", Buy, None, 1, 9),
            order("S", Sell, Some(100), 6, 8),
        ];
        let r = uncross(&buys, None);
        assert_eq!(r, result(100, 6, 4));
        let fills = allocate(&buys, r);
        assert_eq!(
            fills
                .iter()
                .map(|f| (f.buy_id, f.quantity))
                .collect::<Vec<_>>(),
            [("market", 1), ("better", 2), ("old", 3)]
        );
        let sells = [
            order("new", Sell, Some(100), 3, 2),
            order("old", Sell, Some(100), 4, 1),
            order("better", Sell, Some(90), 2, 3),
            order("market", Sell, None, 1, 9),
            order("B", Buy, Some(100), 6, 8),
        ];
        let r = uncross(&sells, None);
        assert_eq!(r, result(100, 6, -4));
        assert_eq!(
            allocate(&sells, r)
                .iter()
                .map(|f| (f.sell_id, f.quantity))
                .collect::<Vec<_>>(),
            [("market", 1), ("better", 2), ("old", 3)]
        );
    }

    #[test]
    fn reference_distance_does_not_overflow() {
        let orders = [
            order("B", Buy, Some(i64::MAX), 1, 1),
            order("S", Sell, Some(i64::MIN), 1, 2),
        ];
        assert_eq!(uncross(&orders, Some(i64::MAX)), result(i64::MAX, 1, 0));
    }

    struct Rng(u64);
    impl Rng {
        fn next(&mut self) -> u64 {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            self.0
        }
    }

    fn counts(orders: &[AuctionOrder<'_>], p: i64) -> (u64, u64) {
        let buy = orders
            .iter()
            .filter(|o| o.side == Buy && o.limit_price.is_none_or(|l| l >= p))
            .map(|o| u64::from(o.quantity))
            .sum();
        let sell = orders
            .iter()
            .filter(|o| o.side == Sell && o.limit_price.is_none_or(|l| l <= p))
            .map(|o| u64::from(o.quantity))
            .sum();
        (buy, sell)
    }

    fn brute_force(orders: &[AuctionOrder<'_>], reference: Option<i64>) -> AuctionResult {
        let net = orders
            .iter()
            .map(|o| i128::from(o.quantity) * if o.side == Buy { 1 } else { -1 })
            .sum();
        let no_cross = result(0, 0, net);
        let limits: Vec<_> = orders.iter().filter_map(|o| o.limit_price).collect();
        if limits.is_empty() {
            let (b, s) = counts(orders, 0);
            return match reference {
                Some(p) if b.min(s) > 0 => result(p, b.min(s), net),
                _ => no_cross,
            };
        }
        let lo = *limits.iter().min().unwrap();
        let hi = *limits.iter().max().unwrap();
        let all: Vec<_> = (lo..=hi)
            .map(|p| {
                let (b, s) = counts(orders, p);
                result(p, b.min(s), i128::from(b) - i128::from(s))
            })
            .collect();
        let max_all = all.iter().map(|r| r.volume).max().unwrap();
        let mut candidates: Vec<_> = all
            .into_iter()
            .filter(|r| limits.contains(&r.price))
            .collect();
        assert_eq!(max_all, candidates.iter().map(|r| r.volume).max().unwrap());
        if max_all == 0 {
            return no_cross;
        }
        candidates.sort_by_key(|r| {
            (
                std::cmp::Reverse(r.volume),
                r.imbalance.abs(),
                reference.map_or(0, |p| (i128::from(r.price) - i128::from(p)).abs()),
            )
        });
        let best = candidates[0];
        candidates.retain(|r| {
            r.volume == best.volume
                && r.imbalance.abs() == best.imbalance.abs()
                && reference.is_none_or(|p| {
                    (i128::from(r.price) - i128::from(p)).abs()
                        == (i128::from(best.price) - i128::from(p)).abs()
                })
        });
        if candidates.iter().all(|r| r.imbalance > 0) {
            *candidates.iter().max_by_key(|r| r.price).unwrap()
        } else {
            *candidates.iter().min_by_key(|r| r.price).unwrap()
        }
    }

    fn priority(o: &AuctionOrder<'_>) -> (bool, i128, u64) {
        (
            o.limit_price.is_some(),
            o.limit_price
                .map_or(0, |p| i128::from(p) * if o.side == Buy { -1 } else { 1 }),
            o.seq,
        )
    }

    #[test]
    fn seeded_books_match_brute_force_and_allocation_invariants() {
        for seed in 1..=4000 {
            let mut rng = Rng(seed * 0x9E37_79B9);
            let ids: Vec<_> = (0..rng.next() % 13).map(|i| i.to_string()).collect();
            let orders: Vec<_> = ids
                .iter()
                .enumerate()
                .map(|(seq, id)| {
                    let side = if rng.next().is_multiple_of(2) {
                        Buy
                    } else {
                        Sell
                    };
                    let price = if rng.next().is_multiple_of(4) {
                        None
                    } else {
                        Some(90 + (rng.next() % 21) as i64)
                    };
                    order(id, side, price, (rng.next() % 10) as u32, seq as u64)
                })
                .collect();
            let reference = if rng.next().is_multiple_of(3) {
                None
            } else {
                Some(88 + (rng.next() % 25) as i64)
            };
            let r = uncross(&orders, reference);
            assert_eq!(r, brute_force(&orders, reference), "seed {seed}");
            let fills = allocate(&orders, r);
            let mut buys = HashMap::<&str, u64>::new();
            let mut sells = HashMap::<&str, u64>::new();
            for f in &fills {
                assert!(f.quantity > 0, "seed {seed}");
                assert_eq!(f.price, r.price, "seed {seed}");
                *buys.entry(f.buy_id).or_default() += u64::from(f.quantity);
                *sells.entry(f.sell_id).or_default() += u64::from(f.quantity);
                assert!(
                    orders.iter().any(|o| o.id == f.buy_id
                        && o.side == Buy
                        && o.limit_price.is_none_or(|p| p >= r.price)),
                    "seed {seed}"
                );
                assert!(
                    orders.iter().any(|o| o.id == f.sell_id
                        && o.side == Sell
                        && o.limit_price.is_none_or(|p| p <= r.price)),
                    "seed {seed}"
                );
            }
            assert_eq!(buys.values().sum::<u64>(), r.volume, "seed {seed}");
            assert_eq!(sells.values().sum::<u64>(), r.volume, "seed {seed}");
            for o in &orders {
                let side_fills = if o.side == Buy { &buys } else { &sells };
                let filled = side_fills.get(o.id).copied().unwrap_or(0);
                assert!(filled <= u64::from(o.quantity), "seed {seed}");
                if filled == 0 {
                    continue;
                }
                for higher in orders.iter().filter(|h| {
                    h.side == o.side
                        && priority(h) < priority(o)
                        && h.limit_price.is_none_or(|p| {
                            if h.side == Buy {
                                p >= r.price
                            } else {
                                p <= r.price
                            }
                        })
                }) {
                    assert_eq!(
                        side_fills.get(higher.id).copied().unwrap_or(0),
                        u64::from(higher.quantity),
                        "seed {seed}"
                    );
                }
            }
        }
    }
}
