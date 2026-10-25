// SPDX-License-Identifier: MIT
use std::collections::BTreeMap;

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn symbol_rules_parse_and_validate_boundaries() {
        let config = SymbolConfig::parse(
            "P0,0,1,1,1,2147483647\nP2,2,5,10,10,1000\nP4,4,25,1,1,20\nP8,8,1,1,1,20\n",
        )
        .unwrap();
        assert_eq!(config.get("unknown"), SymbolRules::default());
        for (symbol, scale) in [("P0", 0), ("P2", 2), ("P4", 4), ("P8", 8)] {
            assert_eq!(config.get(symbol).price_scale, scale);
        }
        let r = config.get("P2");
        assert!(r.validate_price(105).is_ok());
        assert!(r.validate_price(103).is_err());
        for qty in [0, 1, 11, 1010] {
            assert!(r.validate_quantity(qty).is_err());
        }
        for qty in [10, 1000] {
            assert!(r.validate_quantity(qty).is_ok());
        }
        for symbol in ["P0", "P8"] {
            assert!(config.get(symbol).validate_price(i64::MAX).is_ok());
        }
        for text in [
            "P,9,1,1,1,10",
            "P,2,0,1,1,10",
            "P,2,1,0,1,10",
            "P,2,1,1,20,10",
            "P,2,1,1,1,2147483648",
            "P,2,1,1,1,10\nP,2,1,1,1,10",
        ] {
            assert!(SymbolConfig::parse(text).is_err(), "{text}");
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct SymbolRules {
    pub price_scale: u32,
    pub tick_size: i64,
    pub lot_size: u32,
    pub min_quantity: u32,
    pub max_quantity: u32,
}
impl Default for SymbolRules {
    fn default() -> Self {
        Self {
            price_scale: 2,
            tick_size: 1,
            lot_size: 1,
            min_quantity: 1,
            max_quantity: i32::MAX as u32,
        }
    }
}
impl SymbolRules {
    pub fn validate_price(self, price: i64) -> Result<(), String> {
        if self.tick_size <= 0 || price <= 0 || price % self.tick_size != 0 {
            Err("price must be positive and a multiple of symbol tick size".into())
        } else {
            Ok(())
        }
    }
    pub fn validate_quantity(self, quantity: u32) -> Result<(), String> {
        if self.lot_size == 0
            || quantity < self.min_quantity
            || quantity > self.max_quantity
            || !quantity.is_multiple_of(self.lot_size)
        {
            Err("quantity outside symbol lot/min/max rules".into())
        } else {
            Ok(())
        }
    }
}
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SymbolConfig(BTreeMap<String, SymbolRules>);
impl SymbolConfig {
    pub fn get(&self, symbol: &str) -> SymbolRules {
        self.0.get(symbol).copied().unwrap_or_default()
    }
    pub fn load_env() -> Result<Self, String> {
        match std::env::var("FXP_SYMBOL_CONFIG") {
            Ok(path) if !path.is_empty() => {
                Self::parse(&std::fs::read_to_string(path).map_err(|e| e.to_string())?)
            }
            Ok(_) | Err(std::env::VarError::NotPresent) => Ok(Self::default()),
            Err(e) => Err(e.to_string()),
        }
    }
    pub fn canonical(&self) -> String {
        self.0
            .iter()
            .map(|(s, r)| {
                format!(
                    "{s},{},{},{},{},{}\n",
                    r.price_scale, r.tick_size, r.lot_size, r.min_quantity, r.max_quantity
                )
            })
            .collect()
    }
    pub fn parse(text: &str) -> Result<Self, String> {
        let mut result = Self::default();
        for (line_no, line) in text.lines().enumerate() {
            let line = line.trim();
            if line.is_empty()
                || line.starts_with('#')
                || line == "symbol,price_scale,tick_size,lot_size,min_quantity,max_quantity"
            {
                continue;
            }
            let parse = || -> Option<(String, SymbolRules)> {
                let cols: Vec<_> = line.split(',').map(str::trim).collect();
                if cols.len() != 6 {
                    return None;
                }
                let symbol = cols[0];
                if symbol.is_empty()
                    || !symbol
                        .bytes()
                        .all(|c| c.is_ascii_alphanumeric() || b"._-/:".contains(&c))
                {
                    return None;
                }
                if !cols[1..]
                    .iter()
                    .all(|s| !s.is_empty() && s.bytes().all(|c| c.is_ascii_digit()))
                {
                    return None;
                }
                let r = SymbolRules {
                    price_scale: cols[1].parse().ok()?,
                    tick_size: cols[2].parse().ok()?,
                    lot_size: cols[3].parse().ok()?,
                    min_quantity: cols[4].parse().ok()?,
                    max_quantity: cols[5].parse().ok()?,
                };
                if r.price_scale > 8
                    || r.tick_size <= 0
                    || r.lot_size == 0
                    || r.min_quantity == 0
                    || r.min_quantity > r.max_quantity
                    || r.max_quantity > i32::MAX as u32
                    || u64::from(r.min_quantity).div_ceil(u64::from(r.lot_size))
                        * u64::from(r.lot_size)
                        > u64::from(r.max_quantity)
                {
                    return None;
                }
                Some((symbol.into(), r))
            };
            let (symbol, rules) = parse()
                .ok_or_else(|| format!("invalid symbol configuration at line {}", line_no + 1))?;
            if result.0.insert(symbol, rules).is_some() {
                return Err(format!("duplicate symbol at line {}", line_no + 1));
            }
        }
        Ok(result)
    }
}

#[cfg(test)]
mod engine_tests {
    use super::*;
    use crate::{engine::*, fxp};
    #[derive(Clone)]
    struct TestClock;
    impl Clock for TestClock {
        fn now(&self) -> Time {
            Time {
                seconds: 0,
                nanos: 0,
            }
        }
    }
    struct Ids(u64);
    impl ExecIds for Ids {
        fn next_id(&mut self) -> String {
            self.0 += 1;
            format!("R{}", self.0)
        }
    }
    #[test]
    fn engine_enforces_symbol_rules_before_entry_and_replace() {
        let rules = SymbolConfig::parse("TEST,4,5,10,10,100")
            .unwrap()
            .get("TEST");
        let mut engine = Engine::new("TEST", TestClock, Ids(0), None).with_symbol_rules(rules);
        let order = fxp::NewOrderSingle {
            order_id: "O".into(),
            sender: "T".into(),
            symbol: "TEST".into(),
            side: fxp::OrderSide::Buy as i32,
            order_type: fxp::OrderType::Limit as i32,
            time_in_force: fxp::TimeInForce::Gtc as i32,
            quantity: 20,
            price: 105,
            ..Default::default()
        };
        for (price, qty) in [(103, 20), (105, 11), (105, 110)] {
            let out = engine.new_order(
                fxp::NewOrderSingle {
                    price,
                    quantity: qty,
                    ..order.clone()
                },
                0,
            );
            assert!(out.iter().any(|o|matches!(o,Output::ExecutionReport(_,r) if r.execution_type==fxp::ExecutionType::ExecRejected as i32)));
        }
        engine.new_order(order, 0);
        let out = engine.cancel_replace(CancelReplaceMsg {
            new_display_quantity: None,
            order_id: "O".into(),
            sender: "T".into(),
            new_order_id: "N".into(),
            client_replace_id: "N".into(),
            new_price: 106,
            new_quantity: 20,
            new_time_in_force: 0,
            new_stop_price: 0,
            output_tx: 0,
        });
        assert!(out.iter().any(|o|matches!(o,Output::ExecutionReport(_,r) if r.execution_type==fxp::ExecutionType::ExecRejected as i32)));
        assert_eq!(engine.order_status("T", "O", 0)[0].leaves_quantity, 20);
    }
}
