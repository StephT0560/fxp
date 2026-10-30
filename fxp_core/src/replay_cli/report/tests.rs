// SPDX-License-Identifier: MIT
use super::*;
#[test]
fn html_never_embeds_untrusted_markup_and_has_hashed_csp() {
    let data = Json::object([(
        "hostile",
        Json::string("</script><img src=x onerror=alert(1)>\u{2028}"),
    )]);
    let page = html(&data);
    assert!(!page.contains("<img src=x"));
    assert!(page.contains("script-src 'sha256-"));
    assert!(page.contains("\\u003c/script\\u003e"));
}
use crate::{
    engine::Time,
    event_log::{Input, Journal},
    fxp,
    order_book::order::StpMode,
    symbol_config::SymbolConfig,
};
fn fixture(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/bundles")
        .join(name)
}
fn config(bundle: PathBuf) -> Config {
    Config {
        bundle,
        owner: Some("alice".into()),
        to: u64::MAX,
        ..Default::default()
    }
}
#[test]
fn reports_snapshot_all_golden_bundles() {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/report-snapshots");
    let update = std::env::var("FXP_UPDATE_REPORT_GOLDENS").as_deref() == Ok("1");
    if update {
        fs::create_dir_all(&dir).unwrap();
    }
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
        let (data, code) = generate(&config(fixture(name))).unwrap();
        assert_eq!(code, 0, "{name}");
        let encoded = format!("{}\n", data.encode());
        let path = dir.join(format!("{name}.json"));
        if update {
            fs::write(&path, &encoded).unwrap();
        }
        assert_eq!(encoded, fs::read_to_string(path).unwrap(), "{name}");
    }
}
#[test]
fn exact_numbers_anonymization_and_window_prefix() {
    let mut c = config(fixture("partial-fill"));
    c.from = 2;
    c.to = 2;
    c.anonymize = true;
    let (data, code) = generate(&c).unwrap();
    assert_eq!(code, 0);
    let encoded = data.encode();
    assert!(!encoded.contains("bob"));
    assert!(encoded.contains("owner-0002"));
    assert!(!encoded.contains("protobuf_hex"));
    assert!(!encoded.contains("canonical_hex"));
    let Json::Array(books) = data.get("books").unwrap() else {
        panic!()
    };
    let first = books.first().unwrap();
    assert_eq!(text(first, "seq"), "1");
    let Json::Array(bids) = first.get("ladder").unwrap().get("bids").unwrap() else {
        panic!()
    };
    assert_eq!(text(&bids[0], "quantity"), "10");
    let mut j = Json::Number("18446744073709551615".into());
    exact(&mut j);
    assert_eq!(j, Json::string("18446744073709551615"));
    for (input, want) in [
        ("", ""),
        ("f", "Zg=="),
        ("fo", "Zm8="),
        ("foo", "Zm9v"),
        ("foobar", "Zm9vYmFy"),
    ] {
        assert_eq!(base64(input.as_bytes()), want);
    }
}
fn temporary(name: &str) -> PathBuf {
    static N: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let p = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../target/report-tests")
        .join(format!(
            "{}-{name}-{}",
            std::process::id(),
            N.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
    fs::create_dir_all(&p).unwrap();
    p
}
fn order(id: &str, owner: &str) -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        symbol: "TEST".into(),
        sender: owner.into(),
        order_id: id.into(),
        client_order_id: format!("client-{id}"),
        price: 100,
        quantity: 3,
        side: 1,
        order_type: 2,
        time_in_force: 2,
        ..Default::default()
    }
}
#[test]
fn hostile_identity_is_inert_and_anonymized_even_in_namespaced_ids() {
    let dir = temporary("hostile");
    let bundle = dir.join("bundle");
    let hostile = "other</script><img src=https://invalid.test/x onerror=globalThis.__xss=1>";
    let mut j = Journal::record(
        &bundle,
        None,
        StpMode::None,
        (),
        SymbolConfig::default(),
        r#"{"private":"other secret"}"#,
    )
    .unwrap();
    j.apply(
        "TEST",
        Time {
            seconds: 1,
            nanos: 0,
        },
        Input::New(order(&format!("{hostile}::ORDER"), hostile)),
        (),
    )
    .unwrap();
    j.finish().unwrap();
    drop(j);
    let (data, _) = generate(&Config {
        bundle: bundle.clone(),
        to: u64::MAX,
        ..Default::default()
    })
    .unwrap();
    let page = html(&data);
    assert!(!page.contains("<img src=https://"));
    assert!(page.contains("\\u003cimg"));
    let output = Path::new(env!("CARGO_MANIFEST_DIR")).join("../target/report-smoke");
    fs::create_dir_all(&output).unwrap();
    fs::write(output.join("hostile.html"), &page).unwrap();
    let (data, _) = generate(&Config {
        bundle,
        anonymize: true,
        to: u64::MAX,
        ..Default::default()
    })
    .unwrap();
    let encoded = data.encode();
    assert!(!encoded.contains(hostile));
    assert!(!encoded.contains("other secret"));
    assert!(encoded.contains("owner-0001"));
}
#[test]
#[ignore = "durable 50000-business-event fixture; run explicitly in CI"]
fn fifty_thousand_business_events_fit_size_and_generation_budget() {
    let dir = temporary("large");
    let bundle = dir.join("bundle");
    let mut j = Journal::record(
        &bundle,
        None,
        StpMode::None,
        (),
        SymbolConfig::default(),
        "{}",
    )
    .unwrap();
    for i in 0..25000 {
        let id = format!("O{i}");
        let at = Time {
            seconds: 1,
            nanos: 0,
        };
        j.apply("TEST", at, Input::New(order(&id, "alice")), ())
            .unwrap();
        j.apply(
            "TEST",
            at,
            Input::Cancel(fxp::OrderCancelRequest {
                symbol: "TEST".into(),
                sender: "alice".into(),
                order_id: id,
                ..Default::default()
            }),
            (),
        )
        .unwrap();
    }
    j.finish().unwrap();
    drop(j);
    let start = std::time::Instant::now();
    let (data, code) = generate(&config(bundle)).unwrap();
    assert_eq!(code, 0);
    let Json::Array(events) = data.get("events").unwrap() else {
        panic!()
    };
    assert_eq!(events.len(), 100000);
    assert!(data.encode().len() < MAX_DATA);
    assert!(html(&data).len() < 100 * 1024 * 1024);
    assert!(
        start.elapsed() < std::time::Duration::from_secs(120),
        "functional generation budget exceeded"
    );
}
#[test]
fn pseudonyms_do_not_collide_with_selected_owner_and_merge_masked_reason_counts() {
    let rows = vec![
        Json::parse(r#"{"owner":"bob","data":{}}"#).unwrap(),
        Json::parse(r#"{"owner":"owner-0001","data":{}}"#).unwrap(),
    ];
    let p = Privacy::new(&rows, Some("owner-0001"));
    assert_ne!(p.owners["bob"], "owner-0001");
    let rows = vec![
        Json::parse(r#"{"owner":"alice","data":{}}"#).unwrap(),
        Json::parse(r#"{"owner":"bob","data":{}}"#).unwrap(),
    ];
    let p = Privacy::new(&rows, Some("alice"));
    let mut data = Json::parse(r#"{"rejects_by_reason":{"bob":1,"owner-0002":2}}"#).unwrap();
    p.apply(&mut data, "");
    assert_eq!(
        number(data.get("rejects_by_reason").unwrap(), "owner-0002"),
        3
    );
}
fn copy_fixture(name: &str) -> PathBuf {
    let p = temporary("copy").join("bundle");
    fs::create_dir_all(&p).unwrap();
    for f in fs::read_dir(fixture(name)).unwrap() {
        let f = f.unwrap();
        if f.file_type().unwrap().is_file() {
            fs::copy(f.path(), p.join(f.file_name())).unwrap();
        }
    }
    p
}
#[test]
fn divergence_incomplete_tamper_versions_and_output_guards() {
    let bundle = copy_fixture("reject");
    let trace = bundle.join("events.jsonl");
    let changed = fs::read_to_string(&trace)
        .unwrap()
        .replace("quantity must be positive", "forged reason");
    fs::write(&trace, &changed).unwrap();
    let mut manifest =
        Json::parse(&fs::read_to_string(bundle.join("manifest.json")).unwrap()).unwrap();
    if let Json::Object(m) = &mut manifest {
        m.insert(
            "trace_sha256".into(),
            Json::string(sha256(changed.as_bytes())),
        );
    }
    fs::write(bundle.join("manifest.json"), manifest.encode()).unwrap();
    let (data, code) = generate(&config(bundle.clone())).unwrap();
    assert_eq!(code, 1);
    assert_eq!(
        text(data.get("verification").unwrap(), "source"),
        "replayed"
    );
    assert!(
        data.get("verification")
            .unwrap()
            .get("first_divergence")
            .unwrap()
            .encode()
            .contains("forged reason")
    );
    if let Json::Object(m) = &mut manifest {
        m.insert("complete".into(), Json::Bool(false));
    }
    fs::write(bundle.join("manifest.json"), manifest.encode()).unwrap();
    let (data, code) = generate(&config(bundle.clone())).unwrap();
    assert_eq!(code, 2);
    assert_eq!(
        text(data.get("verification").unwrap(), "result"),
        "incomplete"
    );
    if let Json::Object(m) = &mut manifest {
        m.insert("bundle_format".into(), Json::string("future"));
    }
    fs::write(bundle.join("manifest.json"), manifest.encode()).unwrap();
    assert!(generate(&config(bundle)).is_err());
    let bundle = copy_fixture("reject");
    fs::write(bundle.join("commands.log"), b"tampered").unwrap();
    assert!(generate(&config(bundle)).is_err());
    let bundle = copy_fixture("reject");
    assert!(save(&bundle.join("bad.html"), &bundle, b"x").is_err());
    let output = temporary("output").join("report.html");
    save(&output, &bundle, b"original").unwrap();
    assert!(save(&output, &bundle, b"replacement").is_err());
    assert_eq!(fs::read(output).unwrap(), b"original");
}
