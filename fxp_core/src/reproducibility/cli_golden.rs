// SPDX-License-Identifier: MIT
//! Explicit deterministic fixtures; regeneration is opt-in and never part of normal tests.
use super::*;
use crate::engine::Time;
use crate::event_log::{Input, Journal};
use crate::fxp;
fn order(id: &str, owner: &str, side: i32, quantity: i32) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        symbol: "TEST".into(),
        sender: owner.into(),
        order_id: id.into(),
        client_order_id: format!("c-{id}"),
        price: 100,
        quantity,
        side,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}
#[test]
fn regenerate_cli_goldens() {
    if std::env::var("FXP_UPDATE_GOLDENS").as_deref() != Ok("1") {
        return;
    }
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/bundles");
    fs::create_dir_all(&root).unwrap();
    for name in [
        "partial-fill",
        "stp",
        "reject",
        "expiry",
        "auction",
        "iceberg",
        "mass-cancel",
        "stop",
        "replace",
        "disconnect",
    ] {
        let dir = root.join(name);
        if dir.exists() {
            fs::remove_dir_all(&dir).unwrap();
        }
        let schedule = match name {
            "expiry" => Some("close=00:00:05".parse().unwrap()),
            "auction" => Some(
                "open_call=00:00:01,open=00:00:05,close_call=00:00:10,close=00:00:15"
                    .parse()
                    .unwrap(),
            ),
            _ => None,
        };
        let mut j = Journal::record(
            &dir,
            schedule,
            StpMode::None,
            (),
            SymbolConfig::default(),
            "{\"fixture\":true}",
        )
        .unwrap();
        let mut commands: Vec<(i64, Input)> = Vec::new();
        match name {
            "partial-fill" => {
                commands.push((1, Input::New(order("B", "alice", 1, 10))));
                let mut sell = order("S", "bob", 2, 4);
                sell.time_in_force = fxp::TimeInForce::Ioc as i32;
                commands.push((2, Input::New(sell)));
            }
            "stp" => {
                commands.push((1, Input::New(order("S", "alice", 2, 10))));
                let mut buy = order("B", "alice", 1, 5);
                buy.stp_mode = fxp::SelfTradePrevention::CancelNewest as i32;
                commands.push((2, Input::New(buy)));
            }
            "reject" => commands.push((1, Input::New(order("BAD", "alice", 1, -1)))),
            "expiry" => {
                let mut o = order("DAY", "alice", 1, 10);
                o.time_in_force = fxp::TimeInForce::Day as i32;
                commands.push((1, Input::New(o)));
                commands.push((6, Input::Tick));
            }
            "auction" => {
                for (id, owner, side, qty) in [("B", "alice", 1, 10), ("S", "bob", 2, 3)] {
                    let mut o = order(id, owner, side, qty);
                    o.time_in_force = fxp::TimeInForce::AtOpen as i32;
                    commands.push((2, Input::New(o)));
                }
                commands.push((6, Input::Tick));
            }
            "iceberg" => {
                let mut o = order("ICE", "bob", 2, 8);
                o.display_quantity = 2;
                commands.push((1, Input::New(o)));
                let mut b = order("B", "alice", 1, 5);
                b.time_in_force = fxp::TimeInForce::Ioc as i32;
                commands.push((2, Input::New(b)));
            }
            "mass-cancel" | "disconnect" => {
                for id in ["B1", "B2"] {
                    commands.push((1, Input::New(order(id, "alice", 1, 10))));
                }
                commands.push((
                    2,
                    Input::MassCancel(fxp::OrderMassCancelRequest {
                        symbol: "TEST".into(),
                        sender: "alice".into(),
                        client_request_id: "mass-1".into(),
                        reason: if name == "disconnect" {
                            fxp::MassCancelReason::Disconnect as i32
                        } else {
                            fxp::MassCancelReason::Request as i32
                        },
                        ..Default::default()
                    }),
                ));
            }
            "stop" => {
                commands.push((1, Input::New(order("S", "bob", 2, 20))));
                let mut b = order("STOP", "alice", 1, 5);
                b.order_type = fxp::OrderType::Stop as i32;
                b.stop_price = 100;
                commands.push((2, Input::New(b)));
                let mut take = order("TAKE", "carol", 1, 1);
                take.time_in_force = fxp::TimeInForce::Ioc as i32;
                commands.push((3, Input::New(take)));
            }
            "replace" => {
                commands.push((1, Input::New(order("B", "alice", 1, 10))));
                commands.push((
                    2,
                    Input::Replace(fxp::OrderCancelReplaceRequest {
                        symbol: "TEST".into(),
                        sender: "alice".into(),
                        order_id: "B".into(),
                        new_order_id: "B2".into(),
                        client_replace_id: "replace-1".into(),
                        new_price: 101,
                        new_quantity: 12,
                        new_display_quantity: Some(0),
                        ..Default::default()
                    }),
                ));
                commands.push((
                    3,
                    Input::Replace(fxp::OrderCancelReplaceRequest {
                        symbol: "TEST".into(),
                        sender: "alice".into(),
                        order_id: "B2".into(),
                        new_order_id: "BAD".into(),
                        client_replace_id: "replace-bad".into(),
                        new_quantity: -1,
                        ..Default::default()
                    }),
                ));
                commands.push((
                    4,
                    Input::Cancel(fxp::OrderCancelRequest {
                        symbol: "TEST".into(),
                        sender: "alice".into(),
                        order_id: "B2".into(),
                        client_cancel_id: "cancel-1".into(),
                        ..Default::default()
                    }),
                ));
            }
            _ => unreachable!(),
        }
        for (seconds, input) in commands {
            j.apply("TEST", Time { seconds, nanos: 0 }, input, ())
                .unwrap();
        }
        j.finish().unwrap();
        drop(j);
        // Stable fixture provenance; command clocks and actual expected data are unchanged.
        let mut manifest =
            Json::parse(&fs::read_to_string(dir.join("manifest.json")).unwrap()).unwrap();
        if let Json::Object(m) = &mut manifest {
            m.insert("start_time".into(), Json::string("0.000000000"));
            m.insert("end_time".into(), Json::string("0.000000000"));
            m.insert(
                "git_sha".into(),
                Json::string("0000000000000000000000000000000000000000"),
            );
        }
        fs::write(dir.join("manifest.json"), manifest.encode()).unwrap();
        fs::remove_file(dir.join(".recording.lock")).unwrap();
        let target = match name {
            "reject" => "BAD",
            "expiry" => "DAY",
            "iceberg" => "ICE",
            "mass-cancel" | "disconnect" => "B1",
            "stop" => "STOP",
            _ => "B",
        };
        let (mut out, mut err) = (Vec::new(), Vec::new());
        assert_eq!(
            crate::replay_cli::run(
                vec![
                    "explain".into(),
                    dir.to_str().unwrap().into(),
                    "--order".into(),
                    target.into()
                ],
                &mut out,
                &mut err
            ),
            0
        );
        fs::write(dir.join("explain.text"), out).unwrap();
        for format in ["text", "json"] {
            let mut args = vec!["timeline".into(), dir.to_str().unwrap().into()];
            if format == "json" {
                args.push("--json".into());
            }
            let (mut out, mut err) = (Vec::new(), Vec::new());
            assert_eq!(
                crate::replay_cli::run(args, &mut out, &mut err),
                0,
                "{}",
                String::from_utf8_lossy(&err)
            );
            fs::write(dir.join(format!("timeline.{format}")), out).unwrap();
        }
    }
}
