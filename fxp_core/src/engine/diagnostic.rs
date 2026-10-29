// SPDX-License-Identifier: MIT
//! Additive replay-only causal context; never sent on the wire or hashed as business events.
#[derive(Clone, Debug, Default)]
pub(crate) struct Diagnostic {
    pub output_index: usize,
    pub kind: &'static str,
    pub order_id: String,
    pub role: &'static str,
    pub counterparty: String,
    pub quantity: u32,
    pub cause: String,
}
pub(super) fn emit(log: &mut Option<Vec<Diagnostic>>, event: impl FnOnce() -> Diagnostic) {
    if let Some(log) = log {
        log.push(event());
    }
}

#[cfg(test)]
mod tests {
    use crate::engine::{Clock, Engine, ExecIds, Output, Time};
    use crate::fxp;
    struct Fixed;
    impl Clock for Fixed {
        fn now(&self) -> Time {
            Time {
                seconds: 1,
                nanos: 0,
            }
        }
    }
    struct Ids(u64);
    impl ExecIds for Ids {
        fn next_id(&mut self) -> String {
            self.0 += 1;
            format!("X{}", self.0)
        }
    }
    fn order(id: &str, side: i32, qty: i32) -> fxp::NewOrderSingle {
        fxp::NewOrderSingle {
            symbol: "T".into(),
            sender: id.into(),
            order_id: id.into(),
            price: 100,
            quantity: qty,
            side,
            order_type: fxp::OrderType::Limit as i32,
            time_in_force: fxp::TimeInForce::Gtc as i32,
            ..Default::default()
        }
    }
    #[test]
    fn diagnostics_preserve_business_events_and_state_and_report_actual_refreshes() {
        let mut plain = Engine::new("T", Fixed, Ids(0), None);
        let mut detailed = Engine::new("T", Fixed, Ids(0), None).with_diagnostics();
        let mut ice = order("ICE", 2, 8);
        ice.display_quantity = 2;
        let mut buy = order("B", 1, 5);
        buy.time_in_force = fxp::TimeInForce::Ioc as i32;
        for input in [ice, buy] {
            let a = plain.new_order(input.clone(), ());
            let b = detailed.new_order(input, ());
            assert_eq!(
                a.iter()
                    .filter_map(crate::reproducibility::canonical_event)
                    .collect::<Vec<_>>(),
                b.iter()
                    .filter_map(crate::reproducibility::canonical_event)
                    .collect::<Vec<_>>()
            );
            assert_eq!(plain.canonical_state(), detailed.canonical_state());
            assert!(plain.take_diagnostics().is_empty());
        }
        let context = detailed.take_diagnostics();
        assert_eq!(
            context
                .iter()
                .filter(|d| d.kind == "iceberg_refresh")
                .map(|d| d.quantity)
                .collect::<Vec<_>>(),
            vec![2, 2]
        );
        assert!(
            context
                .iter()
                .filter(|d| d.kind == "fill_context" && d.order_id == "B")
                .all(|d| d.role == "aggressor" && d.counterparty == "ICE")
        );
        assert!(
            context
                .iter()
                .filter(|d| d.kind == "fill_context" && d.order_id == "ICE")
                .all(|d| d.role == "resting" && d.counterparty == "B")
        );
        assert!(detailed.take_diagnostics().is_empty());
    }
    #[test]
    fn stop_trigger_precedes_its_fill_report() {
        let mut e = Engine::new("T", Fixed, Ids(0), None).with_diagnostics();
        e.new_order(order("S", 2, 20), ());
        let mut stop = order("STOP", 1, 5);
        stop.order_type = fxp::OrderType::Stop as i32;
        stop.stop_price = 100;
        e.new_order(stop, ());
        let output = e.new_order(order("TAKE", 1, 1), ());
        let diagnostics = e.take_diagnostics();
        let trigger = diagnostics
            .iter()
            .find(|d| d.kind == "stop_trigger")
            .unwrap();
        let fill=output.iter().position(|o|matches!(o,Output::ExecutionReport(_,r) if r.order_id=="STOP"&&r.executed_quantity>0)).unwrap();
        assert!(trigger.output_index <= fill);
        assert_eq!(trigger.order_id, "STOP");
    }
}
