// SPDX-License-Identifier: MIT
use super::*;
use crate::event_log::{Input, Journal, RecordedClock, ReplayIds, tests::TestDir};
use crate::order_book::order::StpMode;
use std::collections::BTreeMap;

type PersistentEngine = Engine<u32, RecordedClock, ReplayIds>;
fn at(seconds: i64) -> Time {
    Time { seconds, nanos: 0 }
}
fn order(id: &str, tif: fxp::TimeInForce) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        symbol: "TEST".into(),
        sender: "owner".into(),
        order_id: id.into(),
        price: 100,
        quantity: 10,
        side: fxp::OrderSide::Buy as i32,
        account: "account".into(),
        client_order_id: format!("client-{id}"),
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: tif as i32,
        expire_date: "1970-02-01".into(),
        ..Default::default()
    }
}

fn fingerprint(e: &mut PersistentEngine) -> String {
    let ids: Vec<_> = e.stop_book.ids().map(str::to_string).collect();
    let mut stops = Vec::new();
    for id in ids {
        let order = e.stop_book.cancel_for(&id, &e.states[&id].sender).unwrap();
        stops.push(order.clone());
        e.stop_book.park(order);
    }
    let mut states: Vec<_> = e
        .states
        .iter()
        .map(|(id, s)| {
            format!(
                "{id} {:?} {:?} {:?} {} {} {} {} {} {:?} {:?}",
                s.sender,
                s.time_in_force,
                s.expire_date,
                s.arrival,
                s.side,
                s.order_qty,
                s.cum_qty,
                s.notional,
                s.account,
                s.client_order_id
            )
        })
        .collect();
    states.sort();
    let mut result = format!(
        "{:?}{:?}{:?}{:?}{:?}",
        e.book.buy_orders, e.book.sell_orders, stops, states, e.phase
    );
    result.push_str(&format!(
        "{:?}{:?}{:?}{:?}",
        e.book.order_lookup.iter().collect::<BTreeMap<_, _>>(),
        e.book.cum_filled.iter().collect::<BTreeMap<_, _>>(),
        e.book.arrival.iter().collect::<BTreeMap<_, _>>(),
        e.last_trade_price
    ));
    result.push_str(&format!(
        "{:?}{:?}{:?}{:?}{}",
        e.call_markets.iter().collect::<BTreeMap<_, _>>(),
        e.held_close.iter().collect::<BTreeMap<_, _>>(),
        e.last_seen,
        e.last_indicative,
        e.next_arrival
    ));
    result
}

fn rng(seed: &mut u64) -> u64 {
    *seed ^= *seed << 13;
    *seed ^= *seed >> 7;
    *seed ^= *seed << 17;
    *seed
}

#[test]
fn seeded_live_history_replays_all_engine_state_and_global_next_exec_id() {
    for seed in 1..=200 {
        seeded_history(seed);
    }
}

fn seeded_history(seed: u64) {
    let dir = TestDir::new();
    let copied = dir.0.join("copied.log");
    let schedule = Some(
        "open_call=00:00:10,open=00:00:20,close_call=00:00:30,close=00:00:40"
            .parse()
            .unwrap(),
    );
    let mut live = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    let mut seed = seed.wrapping_mul(0x9e3779b97f4a7c15) | 1;
    let mut ids: HashMap<&str, Vec<String>> = HashMap::new();
    let mut executed = 0;
    for i in 0..240 {
        let random = rng(&mut seed);
        let symbol = if random & 1 == 0 { "TEST" } else { "OTHER" };
        let seconds = (i / 120) * 86400 + [11, 21, 31, 41][((i % 120) / 30) as usize];
        let time = Time {
            seconds,
            nanos: ((i % 30) * 1_000) as i32,
        };
        let known = ids.entry(symbol).or_default();
        let input = match random % 10 {
            0 => Input::Tick,
            1 => Input::Subscribe,
            4 => Input::MassCancel(fxp::OrderMassCancelRequest {
                symbol: symbol.into(),
                sender: "owner".into(),
                side: (random % 3) as i32,
                ..Default::default()
            }),
            2 if !known.is_empty() => Input::Cancel(fxp::OrderCancelRequest {
                symbol: symbol.into(),
                sender: if random & 16 == 0 { "other" } else { "owner" }.into(),
                order_id: known[(random as usize / 10) % known.len()].clone(),
                client_cancel_id: format!("cancel-{i}"),
                ..Default::default()
            }),
            3 if !known.is_empty() => {
                let old = known[(random as usize / 10) % known.len()].clone();
                let new = format!("replacement-{i}");
                known.push(new.clone());
                Input::Replace(fxp::OrderCancelReplaceRequest {
                    symbol: symbol.into(),
                    sender: "owner".into(),
                    order_id: old,
                    new_order_id: new.clone(),
                    client_replace_id: new,
                    new_price: 95 + (random % 11) as i64,
                    new_quantity: (random % 20) as i32,
                    new_stop_price: 102,
                    ..Default::default()
                })
            }
            _ => {
                let id = format!("order-{i}");
                known.push(id.clone());
                let tif = [
                    fxp::TimeInForce::Day,
                    fxp::TimeInForce::Gtc,
                    fxp::TimeInForce::Gtd,
                    fxp::TimeInForce::AtOpen,
                    fxp::TimeInForce::AtClose,
                    fxp::TimeInForce::Ioc,
                    fxp::TimeInForce::Fok,
                ][(random as usize / 10) % 7];
                Input::New(fxp::NewOrderSingle {
                    symbol: symbol.into(),
                    quantity: 1 + (random % 20) as i32,
                    side: if random & 2 == 0 {
                        fxp::OrderSide::Buy as i32
                    } else {
                        fxp::OrderSide::Sell as i32
                    },
                    order_type: [
                        fxp::OrderType::Market,
                        fxp::OrderType::Limit,
                        fxp::OrderType::Stop,
                        fxp::OrderType::StopLimit,
                    ][(random as usize / 60) % 4] as i32,
                    price: 95 + (random % 11) as i64,
                    stop_price: 102,
                    display_quantity: if random & 32 == 0 { 1 } else { 0 },
                    stp_mode: (random % 6) as i32,
                    stp_group: if random & 64 == 0 {
                        "shared".into()
                    } else {
                        String::new()
                    },
                    ..order(&id, tif)
                })
            }
        };
        let outputs = live.apply(symbol, time, input, 7).unwrap();
        executed += outputs
            .iter()
            .filter(|o| {
                matches!(o, Output::ExecutionReport(_, r)
            if r.execution_type == fxp::ExecutionType::ExecFilled as i32)
            })
            .count();
    }
    let _ = executed;
    let time = at(10 * 86400 + 11);
    for symbol in ["TEST", "OTHER"] {
        for (id, ty, tif) in [
            ("parked", fxp::OrderType::Stop, fxp::TimeInForce::Gtc),
            ("market", fxp::OrderType::Market, fxp::TimeInForce::Gtc),
            ("held", fxp::OrderType::Limit, fxp::TimeInForce::AtClose),
            ("limit", fxp::OrderType::Limit, fxp::TimeInForce::Gtc),
        ] {
            live.apply(
                symbol,
                time,
                Input::New(fxp::NewOrderSingle {
                    symbol: symbol.into(),
                    order_type: ty as i32,
                    stop_price: 102,
                    ..order(id, tif)
                }),
                7,
            )
            .unwrap();
        }
    }
    std::fs::copy(dir.log(), &copied).unwrap();
    let mut replayed = Journal::open(&copied, schedule, StpMode::None, 0).unwrap();
    assert_eq!(
        live.trace, replayed.trace,
        "ordered output stream differs, seed {seed}"
    );
    assert_eq!(live.summary(), replayed.summary());
    assert_eq!(live.canonical_state(), replayed.canonical_state());
    assert_eq!(live.symbols(), replayed.symbols());
    for symbol in live.symbols() {
        assert_eq!(
            fingerprint(live.engines.get_mut(&symbol).unwrap()),
            fingerprint(replayed.engines.get_mut(&symbol).unwrap()),
            "{symbol}"
        );
        assert_eq!(live.engines[&symbol].states["held"].output_tx, 7);
        assert_eq!(replayed.engines[&symbol].states["held"].output_tx, 0);
    }
    assert_eq!(
        live.engines.get_mut("TEST").unwrap().ids.next_id(),
        replayed.engines.get_mut("OTHER").unwrap().ids.next_id()
    );
}

#[test]
fn restart_restores_gtc_gtd_partial_fills_and_expires_day_on_first_tick() {
    let dir = TestDir::new();
    let schedule = Some("close=00:00:40".parse().unwrap());
    let mut journal = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    for (id, tif) in [
        ("GTC", fxp::TimeInForce::Gtc),
        ("GTD", fxp::TimeInForce::Gtd),
        ("DAY", fxp::TimeInForce::Day),
    ] {
        journal
            .apply("TEST", at(21), Input::New(order(id, tif)), 7)
            .unwrap();
    }
    journal
        .apply(
            "TEST",
            at(21),
            Input::New(fxp::NewOrderSingle {
                side: fxp::OrderSide::Sell as i32,
                quantity: 4,
                ..order("SELL", fxp::TimeInForce::Gtc)
            }),
            8,
        )
        .unwrap();
    let before = fingerprint(journal.engines.get_mut("TEST").unwrap());
    drop(journal);
    let mut restarted = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    assert_eq!(
        before,
        fingerprint(restarted.engines.get_mut("TEST").unwrap())
    );
    let out = restarted.tick("TEST", at(41)).unwrap();
    assert!(out.iter().any(|o| matches!(o, Output::ExecutionReport(_, r)
        if r.order_id == "DAY" && r.execution_type == fxp::ExecutionType::ExecExpired as i32)));
    let engine = &restarted.engines["TEST"];
    assert_eq!(engine.book.order_lookup["GTC"].quantity, 6);
    assert_eq!(engine.states["GTC"].cum_qty, 4);
    assert_eq!(engine.states["GTC"].avg_px(), 100);
    assert!(engine.book.order_lookup.contains_key("GTD"));
    assert!(!engine.states.contains_key("DAY"));
    drop(restarted);
    let mut restarted = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    assert!(restarted.tick("TEST", at(41)).unwrap().is_empty());
    let out = restarted
        .tick("TEST", at(session::expiry_instant("1970-02-01").unwrap()))
        .unwrap();
    assert!(out.iter().any(|o| matches!(o, Output::ExecutionReport(_, r)
        if r.order_id == "GTD" && r.execution_type == fxp::ExecutionType::ExecExpired as i32)));
    assert!(restarted.engines["TEST"].states.contains_key("GTC"));
}

#[test]
fn downtime_runs_auction_without_clients_and_catch_up_itself_replays() {
    let dir = TestDir::new();
    let schedule = Some(
        "open_call=00:00:10,open=00:00:20,close_call=00:00:30,close=00:00:40"
            .parse()
            .unwrap(),
    );
    let mut journal = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    journal
        .apply(
            "TEST",
            at(11),
            Input::New(order("BUY", fxp::TimeInForce::Gtc)),
            7,
        )
        .unwrap();
    journal
        .apply(
            "TEST",
            at(11),
            Input::New(fxp::NewOrderSingle {
                side: fxp::OrderSide::Sell as i32,
                quantity: 4,
                ..order("SELL", fxp::TimeInForce::AtOpen)
            }),
            8,
        )
        .unwrap();
    drop(journal);
    let mut restarted = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    let out = restarted.tick("TEST", at(41)).unwrap();
    assert_eq!(
        out.iter()
            .filter(|o| matches!(o, Output::ExecutionReport(owner, r) if *owner == 0 && r.auction))
            .count(),
        2
    );
    assert_eq!(restarted.engines["TEST"].phase(), fxp::TradingPhase::Closed);
    assert_eq!(
        restarted.engines["TEST"].book.order_lookup["BUY"].quantity,
        6
    );
    let expected = fingerprint(restarted.engines.get_mut("TEST").unwrap());
    drop(restarted);
    let mut restarted = Journal::open(&dir.log(), schedule, StpMode::None, 0).unwrap();
    assert_eq!(
        expected,
        fingerprint(restarted.engines.get_mut("TEST").unwrap())
    );
    assert!(restarted.tick("TEST", at(41)).unwrap().is_empty());
}

#[test]
fn symbol_config_is_replayed_and_cannot_change_silently() {
    let dir = TestDir::new();
    let config = crate::symbol_config::SymbolConfig::parse("TEST,8,5,10,10,100").unwrap();
    let mut live =
        Journal::open_configured(&dir.log(), None, StpMode::None, 0, config.clone()).unwrap();
    let mut input = order("O", fxp::TimeInForce::Gtc);
    input.quantity = 20;
    input.price = 105;
    live.apply("TEST", at(0), Input::New(input), 0).unwrap();
    drop(live);
    assert!(Journal::open(&dir.log(), None, StpMode::None, 0).is_err());
    let replay = Journal::open_configured(&dir.log(), None, StpMode::None, 0, config).unwrap();
    assert_eq!(
        replay.engines["TEST"].order_status("owner", "O", 0)[0].leaves_quantity,
        20
    );
}

#[test]
fn torn_final_matching_input_replays_only_committed_prefix_and_next_execution_id() {
    for missing_tail in [1, 4, 10] {
        let dir = TestDir::new();
        let mut j = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
        j.apply(
            "TEST",
            at(1),
            Input::New(order("B", fxp::TimeInForce::Gtc)),
            7,
        )
        .unwrap();
        let prefix = fingerprint(j.engines.get_mut("TEST").unwrap());
        let good = std::fs::metadata(dir.log()).unwrap().len();
        let sell = fxp::NewOrderSingle {
            side: fxp::OrderSide::Sell as i32,
            ..order("S", fxp::TimeInForce::Gtc)
        };
        j.apply("TEST", at(2), Input::New(sell), 7).unwrap();
        drop(j);
        let file = std::fs::OpenOptions::new()
            .write(true)
            .open(dir.log())
            .unwrap();
        file.set_len(file.metadata().unwrap().len() - missing_tail)
            .unwrap();
        drop(file);
        let mut replay = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
        assert_eq!(std::fs::metadata(dir.log()).unwrap().len(), good);
        assert_eq!(fingerprint(replay.engines.get_mut("TEST").unwrap()), prefix);
        let out = replay
            .apply(
                "TEST",
                at(3),
                Input::Cancel(fxp::OrderCancelRequest {
                    symbol: "TEST".into(),
                    sender: "owner".into(),
                    order_id: "B".into(),
                    ..Default::default()
                }),
                7,
            )
            .unwrap();
        let first = out
            .iter()
            .find_map(|o| {
                if let Output::ExecutionReport(_, r) = o {
                    Some(r)
                } else {
                    None
                }
            })
            .unwrap();
        assert_eq!(first.execution_id, "X2");
        drop(replay);
        let replay = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
        assert_eq!(replay.symbols(), vec!["TEST"]);
    }
}
