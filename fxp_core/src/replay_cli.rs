// SPDX-License-Identifier: MIT
//! Offline command-line adapter. No network, bot decisions or session reconstruction.
use crate::reproducibility::{
    Inspection, inspect_bundle,
    json::Json,
    sha256,
    trace::{number, text},
};
use std::collections::{BTreeMap, BTreeSet};
use std::io::{self, Write};
use std::path::Path;
mod report;
const USAGE: &str = "fxp-replay verify <bundle>\nfxp-replay timeline <bundle> [--order ID | --client-order-id ID] [--owner OWNER] [--symbol SYMBOL] [--from SEQ] [--to SEQ] [--json]\nfxp-replay explain <bundle> --order ID [--owner OWNER] [--symbol SYMBOL] [--json]\nfxp-replay diff <bundleA> <bundleB> [--json]\nfxp-replay export <bundle> --jsonl [timeline filters]\nfxp-replay report <bundle> --html out.html [--json out.json] [--owner NAME] [--anonymize-owners] [--from SEQ --to SEQ]";
#[derive(Default)]
struct Options {
    command: String,
    paths: Vec<String>,
    order: Option<String>,
    client: Option<String>,
    owner: Option<String>,
    symbol: Option<String>,
    from: Option<u64>,
    to: Option<u64>,
    json: bool,
    jsonl: bool,
}
fn invalid(s: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, s.into())
}
fn parse(args: impl IntoIterator<Item = String>) -> io::Result<Options> {
    let mut it = args.into_iter();
    let command = it.next().ok_or_else(|| invalid(USAGE))?;
    if !["verify", "timeline", "explain", "diff", "export"].contains(&command.as_str()) {
        return Err(invalid(USAGE));
    }
    let mut o = Options {
        command,
        ..Default::default()
    };
    let mut seen = BTreeSet::new();
    while let Some(arg) = it.next() {
        if arg.starts_with('-') {
            if !seen.insert(arg.clone()) {
                return Err(invalid(format!("duplicate option {arg}")));
            }
            if arg == "--json" {
                o.json = true;
                continue;
            }
            if arg == "--jsonl" {
                o.jsonl = true;
                continue;
            }
            if ![
                "--order",
                "--client-order-id",
                "--owner",
                "--symbol",
                "--from",
                "--to",
            ]
            .contains(&arg.as_str())
            {
                return Err(invalid(format!("unknown option {arg}")));
            }
            let value = it
                .next()
                .filter(|v| !v.starts_with("--"))
                .ok_or_else(|| invalid(format!("missing value for {arg}")))?;
            if value.is_empty() {
                return Err(invalid("empty filter value"));
            }
            match arg.as_str() {
                "--order" => o.order = Some(value),
                "--client-order-id" => o.client = Some(value),
                "--owner" => o.owner = Some(value),
                "--symbol" => o.symbol = Some(value),
                "--from" => {
                    o.from = Some(
                        value
                            .parse()
                            .map_err(|_| invalid("--from requires an unsigned sequence"))?,
                    )
                }
                "--to" => {
                    o.to = Some(
                        value
                            .parse()
                            .map_err(|_| invalid("--to requires an unsigned sequence"))?,
                    )
                }
                _ => unreachable!(),
            }
        } else {
            o.paths.push(arg);
        }
    }
    if o.paths.len() != if o.command == "diff" { 2 } else { 1 } {
        return Err(invalid(USAGE));
    }
    if o.order.is_some() && o.client.is_some() {
        return Err(invalid("--order and --client-order-id are alternatives"));
    }
    if o.from.zip(o.to).is_some_and(|(a, b)| a > b) {
        return Err(invalid("--from exceeds --to"));
    }
    if o.command == "explain" && o.order.is_none() {
        return Err(invalid("explain requires --order"));
    }
    if o.command == "export" && !o.jsonl {
        return Err(invalid("export requires --jsonl"));
    }
    if o.json && o.jsonl || o.jsonl && o.command != "export" {
        return Err(invalid("invalid output format"));
    }
    if ["verify", "diff"].contains(&o.command.as_str())
        && [&o.order, &o.client, &o.owner, &o.symbol]
            .into_iter()
            .any(Option::is_some)
    {
        return Err(invalid(
            "filters are only supported by timeline, explain and export",
        ));
    }
    if ["verify", "diff", "explain"].contains(&o.command.as_str())
        && (o.from.is_some() || o.to.is_some())
    {
        return Err(invalid(
            "sequence filters are only supported by timeline and export",
        ));
    }
    if o.command == "verify" && o.json {
        return Err(invalid("verify uses text output"));
    }
    Ok(o)
}
fn redact(row: &Json, owner: Option<&str>) -> Json {
    let mut row = row.clone();
    let scoped = owner.is_some_and(|owner| text(&row, "owner") == owner);
    for field in ["counterparty", "opposing_order"] {
        if let Json::Object(m) = &mut row
            && let Some(Json::Object(data)) = m.get_mut("data")
            && let Some(Json::String(other)) = data.get_mut(field)
            && !other.is_empty()
            && !scoped
        {
            *other = format!("anon:{}", &sha256(other.as_bytes())[..12]);
        }
    }
    row
}
fn selected(run: &Inspection, o: &Options) -> Vec<Json> {
    let rows = run.source_rows();
    let mut ids = BTreeSet::new();
    for row in rows {
        if text(row, "order_id").is_empty() {
            continue;
        }
        if o.order
            .as_deref()
            .is_some_and(|id| text(row, "order_id") == id)
            || o.client
                .as_deref()
                .is_some_and(|id| text(row, "client_order_id") == id)
        {
            ids.insert((
                text(row, "symbol").to_string(),
                text(row, "order_id").to_string(),
                text(row, "owner").to_string(),
            ));
        }
    }
    loop {
        let before = ids.len();
        for row in rows.iter().filter(|r| text(r, "kind") == "replaced") {
            let a = (
                text(row, "symbol").to_string(),
                text(row, "order_id").to_string(),
                text(row, "owner").to_string(),
            );
            let b = (
                a.0.clone(),
                row.get("data")
                    .map(|d| text(d, "new_order_id").to_string())
                    .unwrap_or_default(),
                a.2.clone(),
            );
            if ids.contains(&a) || ids.contains(&b) {
                ids.insert(a);
                if !b.1.is_empty() {
                    ids.insert(b);
                }
            }
        }
        if ids.len() == before {
            break;
        }
    }
    rows.iter()
        .filter(|r| {
            o.symbol.as_deref().is_none_or(|v| text(r, "symbol") == v)
                && o.owner.as_deref().is_none_or(|v| text(r, "owner") == v)
                && o.from.is_none_or(|v| number(r, "global_seq") >= v)
                && o.to.is_none_or(|v| number(r, "global_seq") <= v)
                && (o.order.is_none() && o.client.is_none()
                    || o.client
                        .as_deref()
                        .is_some_and(|v| text(r, "client_order_id") == v)
                    || ids.contains(&(
                        text(r, "symbol").into(),
                        text(r, "order_id").into(),
                        text(r, "owner").into(),
                    )))
        })
        .map(|r| redact(r, o.owner.as_deref()))
        .collect()
}
fn line(row: &Json) -> String {
    let empty = Json::Null;
    let data = row.get("data").unwrap_or(&empty);
    let base = format!(
        "{}:{} {} {} owner={} client={} {}",
        number(row, "global_seq"),
        number(row, "event_index"),
        text(row, "symbol"),
        text(row, "order_id"),
        text(row, "owner"),
        text(row, "client_order_id"),
        text(row, "kind")
    );
    let detail = match text(row, "kind") {
        "command_received" => {
            let mut data = data.clone();
            if let Json::Object(m) = &mut data {
                m.remove("protobuf_hex");
            }
            data.encode()
        }
        "fill" => format!(
            "price={} quantity={} leaves={} role={} counterparty={}{}",
            number(data, "price"),
            number(data, "quantity"),
            number(data, "leaves_quantity"),
            text(data, "role"),
            text(data, "counterparty"),
            if matches!(data.get("auction"), Ok(Json::Bool(true))) {
                " auction"
            } else {
                ""
            }
        ),
        "accepted" => format!(
            "quantity={} leaves={}",
            number(data, "order_quantity"),
            number(data, "leaves_quantity")
        ),
        "replaced" => format!("new_order_id={}", text(data, "new_order_id")),
        "iceberg_refresh" => format!(
            "quantity={} cause={}",
            number(data, "quantity"),
            text(data, "cause")
        ),
        "rejected" | "canceled" | "expired" | "restated" | "stop_trigger" => {
            let opposing = text(data, "opposing_order");
            format!(
                "cause={}{}",
                text(data, "cause"),
                if opposing.is_empty() {
                    String::new()
                } else {
                    format!(" opposing_order={opposing}")
                }
            )
        }
        _ => data.encode(),
    };
    format!("{base} {detail}")
}
fn command_at(rows: &[Json], seq: u64) -> Json {
    rows.iter()
        .find(|r| number(r, "global_seq") == seq && text(r, "kind") == "command_received")
        .and_then(|r| r.get("data").ok())
        .cloned()
        .unwrap_or(Json::Null)
}
fn first_difference<'a>(
    a: &'a [Json],
    b: &'a [Json],
) -> Option<(Option<&'a Json>, Option<&'a Json>)> {
    (0..a.len().max(b.len())).find_map(|i| {
        if a.get(i) != b.get(i) {
            Some((a.get(i), b.get(i)))
        } else {
            None
        }
    })
}
fn divergence(a: &[Json], b: &[Json]) -> Json {
    let Some((expected, actual)) = first_difference(a, b) else {
        return Json::object([
            ("global_seq", Json::Null),
            ("symbol", Json::Null),
            ("input", Json::Null),
            ("expected_event", Json::Null),
            ("actual_event", Json::Null),
            (
                "detail",
                Json::string("no event-level difference; aggregate or final-state mismatch"),
            ),
        ]);
    };
    let row = actual.or(expected).unwrap();
    let seq = number(row, "global_seq");
    Json::object([
        ("global_seq", Json::number(seq)),
        ("symbol", Json::string(text(row, "symbol"))),
        ("input", command_at(b, seq)),
        ("expected_input", command_at(a, seq)),
        (
            "expected_event",
            expected.map_or(Json::Null, |j| redact(j, None)),
        ),
        (
            "actual_event",
            actual.map_or(Json::Null, |j| redact(j, None)),
        ),
    ])
}
fn stats(rows: &[Json]) -> Json {
    let mut orders = 0;
    let mut fills = 0;
    let mut rejects = BTreeMap::<String, u64>::new();
    for row in rows {
        match text(row, "kind") {
            "command_received" if row.get("data").is_ok_and(|d| text(d, "kind") == "new") => {
                orders += 1
            }
            "trade" => fills += 1,
            "rejected" => {
                let cause = row.get("data").map(|d| text(d, "cause")).unwrap_or("");
                *rejects.entry(cause.into()).or_default() += 1;
            }
            _ => (),
        }
    }
    Json::object([
        ("orders", Json::number(orders)),
        ("fills", Json::number(fills)),
        (
            "rejects_by_reason",
            Json::object(rejects.into_iter().map(|(k, v)| (k, Json::number(v)))),
        ),
    ])
}
fn explanation(rows: &[Json], order: &str) -> String {
    let commands: BTreeMap<u64, &Json> = rows
        .iter()
        .filter(|r| text(r, "kind") == "command_received")
        .filter_map(|r| r.get("data").ok().map(|d| (number(r, "global_seq"), d)))
        .collect();
    let missing = Json::Null;
    let mut fills = 0;
    let mut quantity = 0;
    let mut refreshes = 0;
    let mut triggers = 0;
    let mut causes = BTreeMap::<(String, String, String), (u64, u64)>::new();
    for row in rows {
        let data = row.get("data").unwrap();
        match text(row, "kind") {
            "fill" => {
                fills += 1;
                quantity += number(data, "quantity");
            }
            "iceberg_refresh" => refreshes += 1,
            "stop_trigger" => triggers += 1,
            "rejected" | "canceled" | "expired" | "replaced" | "restated" => {
                let mut cause = if text(row, "kind") == "replaced" {
                    format!("new order {}", text(data, "new_order_id"))
                } else {
                    text(data, "cause").to_string()
                };
                if !text(data, "opposing_order").is_empty() {
                    cause.push_str(&format!(
                        "; opposing order {}",
                        text(data, "opposing_order")
                    ));
                }
                let kind = if text(row, "kind") == "rejected" {
                    format!(
                        "{} rejected",
                        text(
                            commands
                                .get(&number(row, "global_seq"))
                                .copied()
                                .unwrap_or(&missing),
                            "kind"
                        )
                    )
                } else {
                    text(row, "kind").to_string()
                };
                let entry = causes
                    .entry((
                        format!(
                            "{} {} owner={}",
                            text(row, "symbol"),
                            text(row, "order_id"),
                            text(row, "owner")
                        ),
                        kind,
                        cause,
                    ))
                    .or_insert((number(row, "global_seq"), 0));
                entry.1 += 1;
            }
            _ => (),
        }
    }
    let mut summary = format!(
        "{order}: {fills} fill reports, {quantity} units; {triggers} stop triggers; {refreshes} iceberg refreshes.\n"
    );
    for row in rows {
        let data = row.get("data").unwrap();
        if text(row, "kind") == "accepted"
            && let Ok(phase) = data.get("session_phase")
        {
            let name = crate::fxp::TradingPhase::try_from(number(data, "session_phase") as i32)
                .map(|p| format!("{p:?}"))
                .unwrap_or_else(|_| phase.encode());
            summary.push_str(&format!(
                "Accepted in session phase {name} (seq {}).\n",
                number(row, "global_seq")
            ));
        }
        if text(row, "kind") == "fill" && matches!(data.get("auction"), Ok(Json::Bool(true))) {
            summary.push_str(&format!(
                "Auction fill at price {} (seq {}).\n",
                number(data, "price"),
                number(row, "global_seq")
            ));
        }
    }
    let mut causes = causes.into_iter().collect::<Vec<_>>();
    causes.sort_by_key(|(_, (seq, _))| *seq);
    for ((id, kind, cause), (seq, count)) in causes {
        summary.push_str(&format!(
            "{id}: {kind}: {cause} (first seq {seq}, count {count}).\n"
        ));
    }
    if fills == 0
        && triggers == 0
        && refreshes == 0
        && !rows.iter().any(|r| {
            matches!(
                text(r, "kind"),
                "rejected" | "canceled" | "expired" | "replaced" | "restated"
            )
        })
    {
        summary.push_str("No terminal cause recorded in this scope.\n");
    }
    summary
}
/// Exit 0 success/equal, 1 divergence, 2 invalid input/bundle or incomplete verification.
/// Kept as an I/O adapter so snapshot tests exercise the exact executable behavior.
pub fn run(
    args: impl IntoIterator<Item = String>,
    out: &mut impl Write,
    err: &mut impl Write,
) -> i32 {
    let args: Vec<_> = args.into_iter().collect();
    if args.len() == 1 && ["--help", "-h"].contains(&args[0].as_str()) {
        return if writeln!(out, "{USAGE}").is_ok() {
            0
        } else {
            2
        };
    }
    let streaming = args
        .first()
        .is_some_and(|s| ["timeline", "explain", "export"].contains(&s.as_str()));
    match execute(args, out, err) {
        Ok(code) => code,
        Err(error) if streaming && error.kind() == io::ErrorKind::BrokenPipe => 0,
        Err(error) => {
            let _ = writeln!(err, "invalid: {error}");
            2
        }
    }
}
fn execute(
    args: impl IntoIterator<Item = String>,
    out: &mut impl Write,
    err: &mut impl Write,
) -> io::Result<i32> {
    let args: Vec<String> = args.into_iter().collect();
    if args.first().is_some_and(|a| a == "report") {
        return report::execute(&args[1..], out);
    }
    let o = parse(args)?;
    let run = inspect_bundle(Path::new(&o.paths[0]))?;
    if o.command == "verify" {
        if !run.complete {
            return Err(invalid("incomplete bundle: replay ran but cannot verify"));
        }
        if run.diverged {
            let detail=run.expected_rows.as_ref().map(|expected|divergence(expected,&run.rows)).unwrap_or_else(||Json::object([("global_seq",Json::Null),("symbol",Json::Null),("input",Json::Null),("expected_event",Json::Null),("actual_event",Json::Null),("detail",Json::string("legacy bundle has aggregate expectations only; first event unavailable"))]));
            writeln!(out, "DIVERGENCE {}", detail.encode())?;
            writeln!(
                out,
                "expected={} actual={}",
                run.expected.as_ref().unwrap().encode(),
                run.summary.json().encode()
            )?;
            return Ok(1);
        }
        writeln!(
            out,
            "IDENTICAL events={} symbols={} global={} state={}",
            run.summary.global.count,
            run.summary.symbols.len(),
            run.summary.global.sha256,
            run.summary.final_state_sha256
        )?;
        return Ok(0);
    }
    if o.command == "diff" {
        let other = inspect_bundle(Path::new(&o.paths[1]))?;
        if !run.complete || !other.complete {
            return Err(invalid("diff requires complete bundles"));
        }
        let a = run.source_rows();
        let b = other.source_rows();
        if run.diverged || other.diverged {
            writeln!(
                err,
                "warning: current replay diverged from a recorded expectation; comparing historical source events"
            )?;
        }
        let different = a != b || run.expected != other.expected;
        let result = Json::object([
            ("different", Json::Bool(different)),
            (
                "first_divergence",
                if different {
                    divergence(a, b)
                } else {
                    Json::Null
                },
            ),
            ("A", stats(a)),
            ("B", stats(b)),
            ("expected_summary", run.expected.clone().unwrap()),
            ("actual_summary", other.expected.clone().unwrap()),
        ]);
        if o.json {
            writeln!(out, "{}", result.encode())?;
        } else {
            writeln!(
                out,
                "{}\nA {}\nB {}",
                if different {
                    format!("DIVERGENCE {}", divergence(a, b).encode())
                } else {
                    "IDENTICAL".into()
                },
                stats(a).encode(),
                stats(b).encode()
            )?;
        }
        if !o.json {
            let state_a = run
                .expected
                .as_ref()
                .unwrap()
                .get("final_state_sha256")?
                .text()?;
            let state_b = other
                .expected
                .as_ref()
                .unwrap()
                .get("final_state_sha256")?
                .text()?;
            if state_a != state_b {
                writeln!(out, "final_state expected={state_a} actual={state_b}")?;
            }
        }
        return Ok(i32::from(different));
    }
    if !run.complete {
        writeln!(
            err,
            "warning: incomplete bundle; committed prefix only, unverified"
        )?;
    }
    if run.diverged {
        writeln!(
            err,
            "warning: replay diverged; displaying {} events",
            run.source()
        )?;
    }
    let rows = selected(&run, &o);
    if o.command == "explain" {
        if rows.is_empty() {
            return Err(invalid("order not found in selected scope"));
        }
        let summary = explanation(&rows, o.order.as_ref().unwrap());
        if o.json {
            writeln!(
                out,
                "{}",
                Json::object([
                    ("order_id", Json::string(o.order.as_ref().unwrap())),
                    ("source", Json::string(run.source())),
                    ("verified", Json::Bool(run.complete && !run.diverged)),
                    ("summary", Json::string(summary)),
                    ("events", Json::Array(rows))
                ])
                .encode()
            )?;
        } else {
            writeln!(
                out,
                "explain source={} verified={}",
                run.source(),
                run.complete && !run.diverged
            )?;
            write!(out, "{summary}")?;
        }
    } else if o.json {
        writeln!(
            out,
            "{}",
            Json::object([
                ("schema", Json::string("fxp-timeline-v1")),
                ("source", Json::string(run.source())),
                ("verified", Json::Bool(run.complete && !run.diverged)),
                ("events", Json::Array(rows))
            ])
            .encode()
        )?;
    } else if o.jsonl {
        for row in rows {
            writeln!(out, "{}", row.encode())?;
        }
    } else {
        writeln!(
            out,
            "timeline source={} verified={}",
            run.source(),
            run.complete && !run.diverged
        )?;
        for row in rows {
            writeln!(out, "{}", line(&row))?;
        }
    }
    Ok(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    fn fixture(rows: Vec<Json>) -> Inspection {
        Inspection {
            complete: true,
            diverged: false,
            expected: None,
            expected_rows: None,
            rows,
            final_state: Vec::new(),
            summary: crate::reproducibility::RunSummary {
                global: crate::reproducibility::EventDigest {
                    count: 0,
                    sha256: String::new(),
                },
                symbols: BTreeMap::new(),
                final_state_sha256: String::new(),
            },
        }
    }
    fn row(kind: &str, id: &str, owner: &str, client: &str) -> Json {
        Json::object([
            ("global_seq", Json::number(1)),
            ("event_index", Json::number(1)),
            ("symbol", Json::string("T")),
            ("kind", Json::string(kind)),
            ("order_id", Json::string(id)),
            ("owner", Json::string(owner)),
            ("client_order_id", Json::string(client)),
            ("data", Json::object([] as [(&str, Json); 0])),
        ])
    }
    #[test]
    fn opposing_stp_order_is_anonymized_without_owner_scope() {
        let r = Json::parse(r#"{"owner":"alice","data":{"opposing_order":"OWN-B"}}"#).unwrap();
        assert!(text(redact(&r, None).get("data").unwrap(), "opposing_order").starts_with("anon:"));
        assert_eq!(
            text(
                redact(&r, Some("alice")).get("data").unwrap(),
                "opposing_order"
            ),
            "OWN-B"
        );
        assert!(
            text(
                redact(&r, Some("bob")).get("data").unwrap(),
                "opposing_order"
            )
            .starts_with("anon:")
        );
    }
    #[test]
    fn explanation_includes_recorded_stp_restatement() {
        use crate::engine::{Clock, Engine, ExecIds, Time};
        use crate::{event_log::Input, fxp, reproducibility::trace::Collector};
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
                self.0.to_string()
            }
        }
        let mut engine = Engine::new("T", Fixed, Ids(0), None).with_diagnostics();
        let mut trace = Collector::default();
        for (i, (id, side, quantity)) in [("SELL", 2, 10), ("BUY", 1, 3)].into_iter().enumerate() {
            let order = fxp::NewOrderSingle {
                symbol: "T".into(),
                sender: "alice".into(),
                order_id: id.into(),
                client_order_id: id.into(),
                price: 100,
                quantity,
                side,
                order_type: fxp::OrderType::Limit as i32,
                time_in_force: fxp::TimeInForce::Gtc as i32,
                stp_mode: fxp::SelfTradePrevention::DecrementAndCancel as i32,
                ..Default::default()
            };
            let outputs = engine.new_order(order.clone(), ());
            trace.capture(
                i as u64 + 1,
                "T",
                Fixed.now(),
                &Input::New(order),
                &outputs,
                engine.take_diagnostics(),
            );
        }
        let rows = trace
            .rows
            .into_iter()
            .filter(|r| text(r, "order_id") == "SELL")
            .collect::<Vec<_>>();
        let restated = rows
            .iter()
            .find(|r| text(r, "kind") == "restated")
            .expect("surviving STP reduction");
        assert_eq!(number(restated.get("data").unwrap(), "leaves_quantity"), 7);
        let summary = explanation(&rows, "SELL");
        assert!(
            summary.contains("restated: self-trade prevention (decrement-and-cancel)"),
            "{summary}"
        );
        assert!(!summary.contains("No terminal cause"));
        assert!(summary.contains("opposing order BUY"), "{summary}");
    }
    #[test]
    fn client_filter_does_not_cross_owners_reusing_an_order_id() {
        let run = fixture(vec![
            row("accepted", "R", "alice", "ca"),
            row("accepted", "R", "bob", "cb"),
        ]);
        let rows = selected(
            &run,
            &Options {
                client: Some("ca".into()),
                ..Default::default()
            },
        );
        assert_eq!(rows.len(), 1);
        assert_eq!(text(&rows[0], "owner"), "alice");
        assert_eq!(
            selected(
                &run,
                &Options {
                    order: Some("R".into()),
                    ..Default::default()
                }
            )
            .len(),
            2
        );
    }
    #[test]
    fn missing_order_id_does_not_alias_unrelated_public_events() {
        let run = fixture(vec![
            row("rejected", "", "alice", "bad"),
            row("phase", "", "", ""),
            row("trade", "", "", ""),
        ]);
        let rows = selected(
            &run,
            &Options {
                client: Some("bad".into()),
                ..Default::default()
            },
        );
        assert_eq!(rows.len(), 1);
        assert_eq!(text(&rows[0], "kind"), "rejected");
    }
}
