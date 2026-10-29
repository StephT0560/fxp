// SPDX-License-Identifier: MIT
use super::*;
use crate::fxp;
use std::path::Path;
#[test]
fn independent_python_event_vectors() {
    let script = Path::new(env!("CARGO_MANIFEST_DIR")).join("../scripts/repro_vectors.py");
    let result = std::process::Command::new("python3")
        .arg(script)
        .arg("--lines")
        .output()
        .unwrap();
    assert!(
        result.status.success(),
        "{}",
        String::from_utf8_lossy(&result.stderr)
    );
    let output = String::from_utf8(result.stdout).unwrap();
    let vectors = output
        .lines()
        .map(|line| line.split_once('=').unwrap())
        .collect::<BTreeMap<_, _>>();
    struct Clock;
    impl crate::engine::Clock for Clock {
        fn now(&self) -> crate::engine::Time {
            crate::engine::Time {
                seconds: 42,
                nanos: 123,
            }
        }
    }
    struct Ids;
    impl crate::engine::ExecIds for Ids {
        fn next_id(&mut self) -> String {
            "X1".into()
        }
    }
    let engine = crate::engine::Engine::<(), _, _>::new("A", Clock, Ids, None);
    let state = engine.canonical_state();
    assert_eq!(
        *vectors.get("state_empty_engine").unwrap(),
        state.iter().map(|b| format!("{b:02x}")).collect::<String>()
    );
    assert_eq!(
        *vectors.get("state_empty_engine_sha256").unwrap(),
        sha256(&state)
    );
    let outputs: Vec<Output<()>> = vec![
        Output::ExecutionReport(
            (),
            fxp::ExecutionReport {
                execution_id: "X1".into(),
                order_id: "Oé".into(),
                symbol: "A".into(),
                execution_type: 1,
                order_status: 1,
                execution_price: 123,
                executed_quantity: 2,
                timestamp: Some(prost_types::Timestamp {
                    seconds: 42,
                    nanos: 123,
                }),
                leaves_quantity: 8,
                avg_px: 123,
                cum_quantity: 2,
                account: "acct".into(),
                client_order_id: "c".into(),
                side: 1,
                order_quantity: 10,
                display_quantity: 3,
                ..Default::default()
            },
        ),
        Output::Trade(fxp::TradeCaptureReport {
            trade_id: "T1".into(),
            symbol: "A".into(),
            trade_price: 123,
            trade_quantity: 2,
            timestamp: Some(prost_types::Timestamp {
                seconds: 42,
                nanos: 123,
            }),
            aggressor_side: 1,
            seq: 1,
            ..Default::default()
        }),
        Output::TradingSessionStatus(fxp::TradingSessionStatus {
            symbol: "A".into(),
            phase: 2,
            timestamp: Some(prost_types::Timestamp {
                seconds: 42,
                nanos: 123,
            }),
        }),
        Output::AuctionInfo(fxp::AuctionInfo {
            symbol: "A".into(),
            phase: 1,
            indicative_price: 123,
            indicative_volume: 5,
            imbalance_quantity: 2,
            imbalance_side: 2,
            reference_price: 120,
            timestamp: Some(prost_types::Timestamp {
                seconds: 42,
                nanos: 123,
            }),
        }),
    ];
    let mut bytes = Vec::from(b"fxp-business-v1\0".as_slice());
    for (i, event) in outputs.iter().enumerate() {
        let frame = canonical_event(event).unwrap();
        let hex = frame.iter().map(|b| format!("{b:02x}")).collect::<String>();
        assert_eq!(*vectors.get(format!("event_{i}").as_str()).unwrap(), hex);
        assert_eq!(
            *vectors.get(format!("sha256_{i}").as_str()).unwrap(),
            sha256(&frame)
        );
        bytes.extend(frame);
    }
    assert_eq!(*vectors.get("global_sha256").unwrap(), sha256(&bytes));
    let mut a = BusinessDigest::default();
    a.observe("A", &outputs);
    assert_eq!(a.global.result().sha256, sha256(&bytes));
    let mut routed = outputs;
    if let Output::ExecutionReport(_, m) = &mut routed[0] {
        m.header = Some(fxp::FxpHeader {
            protocol_version: "transport".into(),
            sequence_number: 999,
        });
    }
    let mut b = BusinessDigest::default();
    b.observe("A", &routed);
    assert_eq!(a.global.result(), b.global.result());
}
