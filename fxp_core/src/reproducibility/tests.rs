// SPDX-License-Identifier: MIT
use super::*;
use crate::engine::Time;
use crate::event_log::{Input, Journal, tests::TestDir};
use crate::fxp;
fn order(id: &str, symbol: &str, side: i32) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        symbol: symbol.into(),
        sender: format!("owner-{side}"),
        order_id: id.into(),
        price: 100,
        quantity: 10,
        side,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}
#[test]
fn bundle_lifecycle_tamper_versions_and_incomplete() {
    let tmp = TestDir::new();
    let dir = tmp.0.join("run");
    let mut j = Journal::record(
        &dir,
        None,
        StpMode::None,
        0,
        SymbolConfig::default(),
        r#"{"seed":42}"#,
    )
    .unwrap();
    j.apply(
        "A",
        Time {
            seconds: 1,
            nanos: 0,
        },
        Input::New(order("B", "A", 1)),
        1,
    )
    .unwrap();
    let before = j.summary();
    let incomplete = replay_bundle(&dir).unwrap();
    assert!(!incomplete.verified);
    assert_eq!(incomplete.summary, before);
    assert!(!dir.join("expected.json").exists());
    j.finish().unwrap();
    assert!(
        j.tick(
            "A",
            Time {
                seconds: 2,
                nanos: 0
            }
        )
        .err()
        .unwrap()
        .to_string()
        .contains("finalized")
    );
    assert_eq!(replay_bundle(&dir).unwrap().summary, before);
    assert!(replay_bundle(&dir).unwrap().verified);
    let manifest = fs::read_to_string(dir.join("manifest.json")).unwrap();
    for (old, new) in [
        (BUNDLE_VERSION, "fxp-run-v999"),
        (crate::event_log::REPLAY_VERSION, "engine-input-v999"),
        (CANONICAL_VERSION, "fxp-business-v999"),
        (STATE_VERSION, "fxp-state-v999"),
    ] {
        fs::write(dir.join("manifest.json"), manifest.replace(old, new)).unwrap();
        assert!(
            replay_bundle(&dir)
                .unwrap_err()
                .to_string()
                .contains("mismatch")
        );
    }
    fs::write(dir.join("manifest.json"), &manifest).unwrap();
    let original = fs::read(dir.join("commands.log")).unwrap();
    let mut changed = original.clone();
    *changed.last_mut().unwrap() ^= 1;
    fs::write(dir.join("commands.log"), changed).unwrap();
    assert!(
        replay_bundle(&dir)
            .unwrap_err()
            .to_string()
            .contains("commands_sha256 mismatch")
    );
    fs::write(dir.join("commands.log"), &original).unwrap();
    fs::write(dir.join("symbols.csv"), "A,2,1,1,1,20\n").unwrap();
    assert!(
        replay_bundle(&dir)
            .unwrap_err()
            .to_string()
            .contains("symbol_config_sha256 mismatch")
    );
    assert!(Journal::record(&dir, None, StpMode::None, 0, SymbolConfig::default(), "{}").is_err());
}

#[test]
fn replay_child() {
    let Some(dir) = std::env::var_os("FXP_TEST_REPLAY_BUNDLE") else {
        return;
    };
    let result = replay_bundle(Path::new(&dir)).unwrap();
    assert!(result.verified);
    assert_eq!(result.summary.global.count, 40);
}

#[test]
fn same_log_replays_in_twenty_fresh_processes() {
    let tmp = TestDir::new();
    let dir = tmp.0.join("multiprocess");
    let mut j =
        Journal::record(&dir, None, StpMode::None, 0, SymbolConfig::default(), "{}").unwrap();
    for i in 0..20 {
        let symbol = if i % 2 == 0 { "A" } else { "B" };
        j.apply(
            symbol,
            Time {
                seconds: i,
                nanos: 0,
            },
            Input::New(order(&format!("O{i}"), symbol, 1)),
            i,
        )
        .unwrap();
        j.apply(
            symbol,
            Time {
                seconds: i,
                nanos: 1,
            },
            Input::MassCancel(fxp::OrderMassCancelRequest {
                sender: "owner-1".into(),
                symbol: symbol.into(),
                ..Default::default()
            }),
            i,
        )
        .unwrap();
    }
    let expected = j.summary();
    j.finish().unwrap();
    drop(j);
    for _ in 0..20 {
        let result = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "reproducibility::tests::replay_child"])
            .env("FXP_TEST_REPLAY_BUNDLE", &dir)
            .output()
            .unwrap();
        assert!(
            result.status.success(),
            "{}{}",
            String::from_utf8_lossy(&result.stdout),
            String::from_utf8_lossy(&result.stderr)
        );
        assert_eq!(replay_bundle(&dir).unwrap().summary, expected);
    }
}

/// Invoked by scripts/repro_bundle_smoke.sh after simulator shutdown. Using a
/// test entry point keeps the offline API exercised without adding the deferred CLI.
#[test]
fn verify_simulator_bundle() {
    let Some(dir) = std::env::var_os("FXP_TEST_SIM_BUNDLE") else {
        return;
    };
    let result = replay_bundle(Path::new(&dir)).unwrap();
    assert!(result.verified);
    assert!(result.summary.symbols.len() >= 2);
    assert!(result.summary.global.count > 0);
    assert!(result.summary.symbols.values().all(|s| s.count > 0));
    println!(
        "verified {} business events, {} symbols, {}",
        result.summary.global.count,
        result.summary.symbols.len(),
        result.summary.global.sha256
    );
}

#[test]
fn incomplete_torn_tail_is_read_only_and_never_verified() {
    let tmp = TestDir::new();
    let dir = tmp.0.join("crash");
    let mut j =
        Journal::record(&dir, None, StpMode::None, (), SymbolConfig::default(), "{}").unwrap();
    j.apply(
        "A",
        Time {
            seconds: 1,
            nanos: 0,
        },
        Input::New(order("B", "A", 1)),
        (),
    )
    .unwrap();
    let prefix = j.summary();
    j.apply(
        "A",
        Time {
            seconds: 2,
            nanos: 0,
        },
        Input::New(order("S", "A", 2)),
        (),
    )
    .unwrap();
    drop(j);
    let file = OpenOptions::new()
        .write(true)
        .open(dir.join("commands.log"))
        .unwrap();
    let len = file.metadata().unwrap().len() - 3;
    file.set_len(len).unwrap();
    drop(file);
    let replay = replay_bundle(&dir).unwrap();
    assert!(!replay.verified);
    assert_eq!(replay.summary, prefix);
    assert_eq!(fs::metadata(dir.join("commands.log")).unwrap().len(), len);
}

#[test]
fn state_hash_includes_reserves_stops_and_global_exec_counter() {
    let tmp = TestDir::new();
    let dir = tmp.0.join("state");
    let mut j =
        Journal::record(&dir, None, StpMode::None, (), SymbolConfig::default(), "{}").unwrap();
    let initial = j.canonical_state();
    // Independent Python encoding of the empty global state.
    let py=std::process::Command::new("python3").args(["-c","import struct,hashlib; b=b'fxp-state-v1'; print(hashlib.sha256(struct.pack('>Q',len(b))+b+struct.pack('>QQ',1,0)).hexdigest())"]).output().unwrap();
    assert!(py.status.success());
    assert_eq!(
        sha256(&initial),
        String::from_utf8(py.stdout).unwrap().trim()
    );
    let mut o = order("iceberg", "A", 1);
    o.quantity = 20;
    o.display_quantity = 3;
    j.apply(
        "A",
        Time {
            seconds: 1,
            nanos: 0,
        },
        Input::New(o),
        (),
    )
    .unwrap();
    let iceberg = j.summary();
    let mut stop = order("stop", "A", 2);
    stop.order_type = fxp::OrderType::Stop as i32;
    stop.stop_price = 50;
    j.apply(
        "A",
        Time {
            seconds: 2,
            nanos: 0,
        },
        Input::New(stop),
        (),
    )
    .unwrap();
    assert_ne!(iceberg.final_state_sha256, j.summary().final_state_sha256);
    j.finish().unwrap();
    let replay = replay_bundle(&dir).unwrap();
    assert_eq!(replay.final_state, j.canonical_state());
}

#[test]
fn metadata_budget_does_not_limit_generated_bundle_documents() {
    let tmp = TestDir::new();
    let large = "x".repeat(4 * 1024 * 1024 + 1);
    let text = Json::object([("symbols", Json::string(&large))]).encode();
    // A generated per-symbol summary can exceed the caller-metadata budget.
    assert!(Json::parse(&text).is_ok());
    let err = Journal::record(
        &tmp.0.join("oversize"),
        None,
        StpMode::None,
        (),
        SymbolConfig::default(),
        &text,
    )
    .err()
    .unwrap();
    assert!(err.to_string().contains("FXP_RUN_META_JSON"));
    assert!(!tmp.0.join("oversize").exists());
}
