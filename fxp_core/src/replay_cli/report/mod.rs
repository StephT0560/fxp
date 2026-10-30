// SPDX-License-Identifier: MIT
//! Stable, self-contained offline report adapter.
use super::*;
const SCHEMA: &str = "fxp-run-report-v1";
#[cfg(test)]
mod tests;
use crate::reproducibility::json::Json;
use std::fs;
use std::path::PathBuf;
const MAX_INPUT: u64 = 256 * 1024 * 1024;
const MAX_ROWS: usize = 100_000;
const MAX_DATA: usize = 96 * 1024 * 1024;
const MAX_HTML: usize = 100 * 1024 * 1024;
#[derive(Default)]
struct Config {
    bundle: PathBuf,
    html: Option<PathBuf>,
    json: Option<PathBuf>,
    owner: Option<String>,
    anonymize: bool,
    from: u64,
    to: u64,
}
fn options(args: &[String]) -> io::Result<Config> {
    let mut c = Config {
        to: u64::MAX,
        ..Default::default()
    };
    let mut seen = BTreeSet::new();
    let mut it = args.iter();
    c.bundle = it
        .next()
        .filter(|s| !s.starts_with('-'))
        .ok_or_else(|| invalid("report requires a bundle"))?
        .into();
    while let Some(k) = it.next() {
        if !seen.insert(k) {
            return Err(invalid("duplicate report option"));
        }
        if k == "--anonymize-owners" {
            c.anonymize = true;
            continue;
        }
        let v = it
            .next()
            .filter(|s| !s.is_empty() && !s.starts_with("--"))
            .ok_or_else(|| invalid(format!("missing value for {k}")))?;
        match k.as_str() {
            "--html" => c.html = Some(v.into()),
            "--json" => c.json = Some(v.into()),
            "--owner" => c.owner = Some(v.clone()),
            "--from" => c.from = v.parse().map_err(|_| invalid("--from requires u64"))?,
            "--to" => c.to = v.parse().map_err(|_| invalid("--to requires u64"))?,
            _ => return Err(invalid(format!("unknown report option {k}"))),
        }
    }
    if c.html.is_none() && c.json.is_none() || c.from > c.to {
        return Err(invalid(
            "report requires --html and/or --json; --from must not exceed --to",
        ));
    }
    Ok(c)
}
fn count_map(map: BTreeMap<String, u64>) -> Json {
    Json::object(map.into_iter().map(|(k, v)| (k, Json::number(v))))
}
fn summary(rows: &[Json]) -> Json {
    let mut rejects = BTreeMap::new();
    let mut cancels = BTreeMap::new();
    let mut owners = BTreeMap::<String, [u64; 4]>::new();
    let mut orders = 0;
    let mut fills = 0;
    for r in rows {
        let d = r.get("data").unwrap();
        let owner = text(r, "owner");
        let kind = text(r, "kind");
        let new = kind == "command_received" && text(d, "kind") == "new";
        orders += u64::from(new);
        fills += u64::from(kind == "trade");
        if kind == "rejected" {
            *rejects.entry(text(d, "cause").into()).or_insert(0) += 1;
        }
        if matches!(kind, "canceled" | "expired") {
            *cancels.entry(text(d, "cause").into()).or_insert(0) += 1;
        }
        if !owner.is_empty() {
            let n = owners.entry(owner.into()).or_default();
            n[0] += u64::from(new);
            n[1] += u64::from(kind == "fill");
            n[2] += u64::from(kind == "rejected");
            n[3] += u64::from(matches!(kind, "canceled" | "expired"));
        }
    }
    Json::object([
        ("orders", Json::number(orders)),
        ("fills", Json::number(fills)),
        ("rejects_by_reason", count_map(rejects)),
        ("cancels_by_cause", count_map(cancels)),
        (
            "owners",
            Json::Array(
                owners
                    .into_iter()
                    .map(|(o, n)| {
                        Json::object([
                            ("owner", Json::string(o)),
                            ("orders", Json::number(n[0])),
                            ("fill_reports", Json::number(n[1])),
                            ("rejects", Json::number(n[2])),
                            ("cancels", Json::number(n[3])),
                        ])
                    })
                    .collect(),
            ),
        ),
    ])
}
type Key = (String, String, String);
fn key(r: &Json) -> Key {
    (
        text(r, "symbol").into(),
        text(r, "owner").into(),
        text(r, "order_id").into(),
    )
}
fn orders(rows: &[Json], from: u64, to: u64, owner: Option<&str>) -> Vec<Json> {
    // A submission is an attempt, not a permanent (owner, ID) identity. Keep
    // rejected duplicates separate without displacing the existing live chain.
    let mut current = BTreeMap::<Key, usize>::new();
    let mut pending: Option<(u64, Key, usize)> = None;
    let mut groups = Vec::<Vec<&Json>>::new();
    for r in rows {
        let kind = text(r, "kind");
        let d = r.get("data").unwrap();
        let new = kind == "command_received" && text(d, "kind") == "new";
        if text(r, "order_id").is_empty() && !new {
            continue;
        }
        let k = key(r);
        let seq = number(r, "global_seq");
        let group = if new {
            let n = groups.len();
            groups.push(Vec::new());
            pending = Some((seq, k.clone(), n));
            n
        } else if let Some((_, _, n)) = pending
            .as_ref()
            .filter(|(s, key, _)| *s == seq && *key == k)
        {
            *n
        } else {
            *current.entry(k.clone()).or_insert_with(|| {
                let n = groups.len();
                groups.push(Vec::new());
                n
            })
        };
        groups[group].push(r);
        if kind == "accepted" {
            current.insert(k.clone(), group);
        }
        if kind == "replaced" {
            let next = text(d, "new_order_id");
            if !next.is_empty() {
                current.insert((k.0, k.1, next.into()), group);
            }
        }
    }
    groups
        .into_iter()
        .filter_map(|all| {
            let retained: Vec<Json> = all
                .iter()
                .filter(|r| (from..=to).contains(&number(r, "global_seq")))
                .map(|r| redact(r, owner))
                .collect();
            if retained.is_empty() {
                return None;
            }
            let first = all[0];
            let mut status = "pending";
            let mut accepted = false;
            let mut reject = "";
            let mut cancel = "";
            for r in all.iter().filter(|r| number(r, "global_seq") <= to) {
                let d = r.get("data").unwrap();
                match text(r, "kind") {
                    "accepted" => {
                        accepted = true;
                        status = "working";
                    }
                    "fill" => {
                        status = if number(d, "leaves_quantity") == 0 {
                            "filled"
                        } else {
                            "partially_filled"
                        }
                    }
                    "canceled" => {
                        status = "canceled";
                        cancel = text(d, "cause");
                    }
                    "expired" => {
                        status = "expired";
                        cancel = text(d, "cause");
                    }
                    "rejected" => {
                        reject = text(d, "cause");
                        if !accepted {
                            status = "rejected";
                        }
                    }
                    _ => (),
                }
            }
            let aliases: BTreeSet<_> = all
                .iter()
                .filter(|r| number(r, "global_seq") <= to)
                .map(|r| text(r, "order_id"))
                .collect();
            let explanation = super::explanation(&retained, text(first, "order_id"));
            Some(Json::object([
                ("order_id", Json::string(text(first, "order_id"))),
                ("owner", Json::string(text(first, "owner"))),
                ("symbol", Json::string(text(first, "symbol"))),
                ("status", Json::string(status)),
                ("reject_reason", Json::string(reject)),
                ("cancel_cause", Json::string(cancel)),
                (
                    "aliases",
                    Json::Array(aliases.into_iter().map(Json::string).collect()),
                ),
                (
                    "client_order_ids",
                    Json::Array(
                        all.iter()
                            .filter(|r| number(r, "global_seq") <= to)
                            .map(|r| text(r, "client_order_id"))
                            .filter(|s| !s.is_empty())
                            .collect::<BTreeSet<_>>()
                            .into_iter()
                            .map(Json::string)
                            .collect(),
                    ),
                ),
                ("explanation", Json::string(explanation)),
                (
                    "positions",
                    Json::Array(
                        retained
                            .iter()
                            .map(|r| {
                                Json::Array(vec![
                                    Json::number(number(r, "global_seq")),
                                    Json::number(number(r, "event_index")),
                                ])
                            })
                            .collect(),
                    ),
                ),
            ]))
        })
        .collect()
}
// All integer values, including nested timeline values, are decimal strings.
fn exact(j: &mut Json) {
    match j {
        Json::Number(n) => *j = Json::String(n.clone()),
        Json::Array(a) => a.iter_mut().for_each(exact),
        Json::Object(m) => m.values_mut().for_each(exact),
        _ => (),
    }
}
fn trim(j: &mut Json) {
    match j {
        Json::Object(m) => {
            m.remove("canonical_hex");
            m.remove("protobuf_hex");
            for v in m.values_mut() {
                trim(v);
            }
        }
        Json::Array(a) => a.iter_mut().for_each(trim),
        _ => (),
    }
}
#[derive(Default)]
struct MaskNode {
    next: BTreeMap<u8, usize>,
    value: Option<String>,
}
struct Privacy {
    owners: BTreeMap<String, String>,
    identities: BTreeMap<String, String>,
    masks: Vec<MaskNode>,
}
impl Privacy {
    fn new(rows: &[Json], selected: Option<&str>) -> Self {
        let names: BTreeSet<_> = rows
            .iter()
            .map(|r| text(r, "owner"))
            .filter(|s| !s.is_empty())
            .collect();
        let owners: BTreeMap<_, _> = names
            .into_iter()
            .enumerate()
            .filter(|(_, n)| Some(*n) != selected)
            .map(|(i, n)| {
                let mut label = format!("owner-{:04}", i + 1);
                if Some(label.as_str()) == selected {
                    label.push('-');
                }
                (n.into(), label)
            })
            .collect();
        let mut names = BTreeSet::new();
        for r in rows {
            for object in [Some(r), r.get("data").ok()].into_iter().flatten() {
                for k in [
                    "order_id",
                    "new_order_id",
                    "orig_order_id",
                    "client_order_id",
                    "counterparty",
                    "opposing_order",
                ] {
                    let v = text(object, k);
                    if !v.is_empty() && !v.starts_with("anon:") {
                        names.insert(v.to_string());
                    }
                }
            }
        }
        let identities: BTreeMap<_, _> = names
            .into_iter()
            .enumerate()
            .map(|(i, n)| (n, format!("id-{:06}", i + 1)))
            .collect();
        let mut masks = vec![MaskNode::default()];
        for (name, label) in owners.iter().chain(identities.iter()) {
            let mut at = 0;
            for byte in name.as_bytes() {
                at = if let Some(next) = masks[at].next.get(byte) {
                    *next
                } else {
                    let next = masks.len();
                    masks.push(MaskNode::default());
                    masks[at].next.insert(*byte, next);
                    next
                };
            }
            masks[at].value = Some(label.clone());
        }
        Self {
            owners,
            identities,
            masks,
        }
    }
    fn prose(&self, s: &str) -> String {
        let mut out = String::new();
        let mut at = 0;
        while at < s.len() {
            let mut node = 0;
            let mut longest = None;
            for (offset, byte) in s.as_bytes()[at..].iter().enumerate() {
                let Some(next) = self.masks[node].next.get(byte) else {
                    break;
                };
                node = *next;
                if let Some(value) = &self.masks[node].value {
                    longest = Some((offset + 1, value));
                }
            }
            if let Some((len, value)) = longest {
                out.push_str(value);
                at += len;
            } else {
                let c = s[at..].chars().next().unwrap();
                out.push(c);
                at += c.len_utf8();
            }
        }
        out
    }
    fn apply(&self, j: &mut Json, field: &str) {
        match j {
            Json::String(s) => {
                if field == "owner" || field == "selected_owner" {
                    if let Some(v) = self.owners.get(s) {
                        *s = v.clone();
                    }
                } else if [
                    "order_id",
                    "new_order_id",
                    "orig_order_id",
                    "client_order_id",
                    "counterparty",
                    "opposing_order",
                ]
                .contains(&field)
                {
                    if let Some(v) = self.identities.get(s) {
                        *s = v.clone();
                    }
                } else if ![
                    "schema",
                    "symbol",
                    "symbols",
                    "source",
                    "result",
                    "kind",
                    "role",
                    "status",
                    "phase",
                    "session_phase",
                    "bundle_format",
                    "canonical_version",
                    "state_version",
                    "trace_version",
                    "replay_version",
                    "fxp_version",
                ]
                .contains(&field)
                    && !(s.len() >= 40 && s.bytes().all(|b| b.is_ascii_hexdigit()))
                {
                    *s = self.prose(s);
                }
            }
            Json::Array(a) => {
                for v in a {
                    self.apply(v, field);
                }
            }
            Json::Object(m) => {
                m.remove("account");
                m.remove("stp_group");
                let old = std::mem::take(m);
                for (k, mut v) in old {
                    self.apply(&mut v, &k);
                    let k = if ["rejects_by_reason", "cancels_by_cause"].contains(&field) {
                        self.prose(&k)
                    } else {
                        k
                    };
                    if ["rejects_by_reason", "cancels_by_cause"].contains(&field)
                        && let (Some(Json::Number(a)), Json::Number(b)) = (m.get(&k), &v)
                    {
                        v = Json::number(a.parse::<u64>().unwrap() + b.parse::<u64>().unwrap());
                    }
                    m.insert(k, v);
                }
            }
            _ => (),
        }
    }
}
fn generate(c: &Config) -> io::Result<(Json, i32)> {
    let mut bytes = 0;
    for name in [
        "manifest.json",
        "commands.log",
        "events.jsonl",
        "expected.json",
        "symbols.csv",
    ] {
        match fs::metadata(c.bundle.join(name)) {
            Ok(m) => bytes += m.len(),
            Err(e) if e.kind() == io::ErrorKind::NotFound => (),
            Err(e) => return Err(e),
        }
    }
    if bytes > MAX_INPUT {
        return Err(invalid("report input exceeds 256 MiB limit"));
    }
    let run = inspect_bundle(&c.bundle)?;
    let manifest = Json::parse(&fs::read_to_string(c.bundle.join("manifest.json"))?)?;
    let rows = if run.diverged {
        &run.rows
    } else {
        run.source_rows()
    };
    if c.from > rows.last().map_or(0, |r| number(r, "global_seq")) {
        return Err(invalid("--from exceeds the last journal sequence"));
    }
    let retained: Vec<Json> = rows
        .iter()
        .filter(|r| (c.from..=c.to).contains(&number(r, "global_seq")))
        .map(|r| redact(r, c.owner.as_deref()))
        .collect();
    if retained.len() > MAX_ROWS {
        return Err(invalid(
            "report exceeds 100000 timeline rows; narrow --from/--to",
        ));
    }
    let mut frames = Vec::new();
    let mut previous = BTreeMap::new();
    let mut prefix = BTreeMap::new();
    let mut frame_bytes = 0;
    let schedule = match manifest.get("session_schedule")? {
        Json::Null => None,
        j => Some(j.text()?.parse().map_err(invalid)?),
    };
    let config = crate::symbol_config::SymbolConfig::parse(&fs::read_to_string(
        c.bundle.join("symbols.csv"),
    )?)
    .map_err(invalid)?;
    let stp = crate::order_book::order::StpMode::parse(manifest.get("stp_default")?.text()?)
        .map_err(invalid)?;
    let journal = crate::event_log::Journal::replay_observed(
        &c.bundle.join("commands.log"),
        schedule,
        stp,
        (),
        config,
        run.complete,
        false,
        |seq, symbol, engine, _outputs| {
            if seq > c.to {
                return Ok(());
            }
            let view = engine.report_ladder(c.owner.as_deref());
            if seq < c.from {
                prefix.insert(symbol.to_string(), view);
                return Ok(());
            }
            if previous.get(symbol) != Some(&view) {
                let frame = Json::object([
                    ("seq", Json::number(seq)),
                    ("symbol", Json::string(symbol)),
                    ("ladder", view.clone()),
                ]);
                frame_bytes += frame.encode().len();
                if frame_bytes > MAX_DATA / 2 {
                    return Err(invalid("book frames exceed 48 MiB; narrow --from/--to"));
                }
                frames.push(frame);
                previous.insert(symbol.to_string(), view);
            }
            Ok(())
        },
    )?;
    if journal.summary().json() != run.summary.json() {
        return Err(invalid(
            "bundle changed between verification and book replay",
        ));
    }
    let mut initial: Vec<Json> = prefix
        .into_iter()
        .map(|(symbol, view)| {
            Json::object([
                ("seq", Json::number(c.from.saturating_sub(1))),
                ("symbol", Json::string(symbol)),
                ("ladder", view),
            ])
        })
        .collect();
    initial.extend(frames);
    let tape: Vec<Json> = retained
        .iter()
        .filter(|r| text(r, "kind") == "trade")
        .cloned()
        .collect();
    let total_summary = summary(rows);
    let status = if !run.complete {
        "incomplete"
    } else if run.diverged {
        "divergent"
    } else {
        "identical"
    };
    let first = if run.diverged {
        run.expected_rows
            .as_ref()
            .map(|r| divergence(r, &run.rows))
            .unwrap_or_else(|| {
                Json::object([(
                    "detail",
                    Json::string(
                        "legacy bundle has aggregate expectations only; first event unavailable",
                    ),
                )])
            })
    } else {
        Json::Null
    };
    let symbols: BTreeSet<_> = rows.iter().map(|r| text(r, "symbol")).collect();
    let mut manifest = manifest;
    if c.anonymize
        && let Json::Object(m) = &mut manifest
    {
        m.retain(|k, _| {
            [
                "bundle_format",
                "canonical_version",
                "state_version",
                "fxp_version",
                "git_sha",
                "proto_schema_sha256",
                "replay_version",
                "symbol_config_file",
                "symbol_config_sha256",
                "session_schedule",
                "stp_default",
                "meta",
                "start_time",
                "end_time",
                "complete",
                "commands_sha256",
                "trace_version",
                "trace_sha256",
            ]
            .contains(&k.as_str())
        });
        m.insert("meta".into(), Json::string("omitted in anonymized report"));
    }
    let mut data = Json::object([
        ("schema", Json::string(SCHEMA)),
        ("manifest", manifest),
        (
            "verification",
            Json::object([
                ("result", Json::string(status)),
                (
                    "source",
                    Json::string(if run.diverged {
                        "replayed"
                    } else {
                        run.source()
                    }),
                ),
                ("first_divergence", first),
                ("expected", run.expected.clone().unwrap_or(Json::Null)),
                ("actual", run.summary.json()),
            ]),
        ),
        (
            "privacy",
            Json::object([
                ("anonymized_owners", Json::Bool(c.anonymize)),
                (
                    "selected_owner",
                    c.owner.as_deref().map_or(Json::Null, Json::string),
                ),
            ]),
        ),
        (
            "window",
            Json::object([
                ("from", Json::number(c.from)),
                (
                    "to",
                    Json::number(c.to.min(rows.last().map_or(0, |r| number(r, "global_seq")))),
                ),
                ("max_rows", Json::number(MAX_ROWS as u64)),
                ("max_data_bytes", Json::number(MAX_DATA as u64)),
                (
                    "boundary",
                    Json::string(
                        "post-command journal sequence; tape/lifecycles windowed, summary whole run",
                    ),
                ),
            ]),
        ),
        ("summary", total_summary),
        (
            "orders",
            Json::Array(orders(rows, c.from, c.to, c.owner.as_deref())),
        ),
        (
            "symbols",
            Json::Array(symbols.into_iter().map(Json::string).collect()),
        ),
        ("events", Json::Array(retained)),
        ("books", Json::Array(initial)),
        ("tape", Json::Array(tape)),
    ]);
    trim(&mut data);
    if c.anonymize {
        if run.diverged {
            let mut identities = run.rows.clone();
            if let Some(expected) = &run.expected_rows {
                identities.extend(expected.iter().cloned());
            }
            Privacy::new(&identities, c.owner.as_deref()).apply(&mut data, "");
        } else {
            Privacy::new(rows, c.owner.as_deref()).apply(&mut data, "");
        }
    }
    exact(&mut data);
    if data.encode().len() > MAX_DATA {
        return Err(invalid("report JSON exceeds 96 MiB; narrow --from/--to"));
    }
    Ok((
        data,
        if !run.complete {
            2
        } else {
            i32::from(run.diverged)
        },
    ))
}
fn save(path: &Path, bundle: &Path, bytes: &[u8]) -> io::Result<()> {
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    if parent.canonicalize()?.starts_with(bundle.canonicalize()?) {
        return Err(invalid("report output must be outside the bundle"));
    }
    let mut options = fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path)?;
    if let Err(e) = file.write_all(bytes) {
        drop(file);
        let _ = fs::remove_file(path);
        return Err(e);
    }
    Ok(())
}
pub(super) fn execute(args: &[String], out: &mut impl Write) -> io::Result<i32> {
    let c = options(args)?;
    let (data, exit) = generate(&c)?;
    if let Some(p) = &c.html {
        save(p, &c.bundle, bounded_html(&data, MAX_HTML)?.as_bytes())?;
        writeln!(out, "HTML report: {}", p.display())?;
    }
    if let Some(p) = &c.json {
        save(p, &c.bundle, data.encode().as_bytes())?;
        writeln!(out, "JSON report: {}", p.display())?;
    }
    Ok(exit)
}
fn base64(bytes: &[u8]) -> String {
    const A: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for c in bytes.chunks(3) {
        let n = (u32::from(c[0]) << 16)
            | (u32::from(*c.get(1).unwrap_or(&0)) << 8)
            | u32::from(*c.get(2).unwrap_or(&0));
        for i in 0..4 {
            out.push(if i > c.len() {
                '='
            } else {
                A[((n >> (18 - i * 6)) & 63) as usize] as char
            });
        }
    }
    out
}
fn hash(bytes: &[u8]) -> String {
    use sha2::Digest;
    base64(&sha2::Sha256::digest(bytes))
}
fn html(data: &Json) -> String {
    let css = include_str!("style.css")
        .replace("\r\n", "\n")
        .replace('\r', "\n");
    let js = include_str!("view.js")
        .replace("\r\n", "\n")
        .replace('\r', "\n");
    let json = data
        .encode()
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    include_str!("shell.html")
        .replace("__STYLE_HASH__", &hash(css.as_bytes()))
        .replace("__SCRIPT_HASH__", &hash(js.as_bytes()))
        .replace("__CSS__", &css)
        .replace("__SCRIPT__", &js)
        .replace("__DATA__", &json)
}
fn bounded_html(data: &Json, limit: usize) -> io::Result<String> {
    let page = html(data);
    if page.len() > limit {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "HTML report exceeds size limit; narrow the --from/--to window",
        ));
    }
    Ok(page)
}
