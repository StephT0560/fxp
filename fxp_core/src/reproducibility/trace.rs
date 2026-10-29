// SPDX-License-Identifier: MIT
//! Versioned, additive replay inspection trace. Business canonical bytes remain unchanged.
use super::{canonical_event, json::Json};
use crate::engine::{Diagnostic, Output, Time};
use crate::event_log::Input;
use crate::fxp;
use std::collections::BTreeMap;
pub(crate) const VERSION: &str = "fxp-trace-v1";
pub(crate) fn hex(b: &[u8]) -> String {
    b.iter().map(|b| format!("{b:02x}")).collect()
}
pub(crate) fn text<'a>(j: &'a Json, key: &str) -> &'a str {
    j.get(key).ok().and_then(|v| v.text().ok()).unwrap_or("")
}
pub(crate) fn number(j: &Json, key: &str) -> u64 {
    match j.get(key) {
        Ok(Json::Number(s)) => s.parse().unwrap_or(0),
        _ => 0,
    }
}
fn integer(n: i64) -> Json {
    Json::Number(n.to_string())
}
pub(crate) fn command(input: &Input) -> Json {
    let mut m = BTreeMap::new();
    let mut str_field = |k: &str, v: &str| {
        m.insert(k.into(), Json::string(v));
    };
    match input {
        Input::New(o) => {
            for (k, v) in [
                ("kind", "new"),
                ("order_id", o.order_id.as_str()),
                ("owner", o.sender.as_str()),
                ("client_order_id", o.client_order_id.as_str()),
                ("expire_date", o.expire_date.as_str()),
                ("account", o.account.as_str()),
                ("stp_group", o.stp_group.as_str()),
            ] {
                str_field(k, v);
            }
            for (k, v) in [
                ("price", o.price),
                ("quantity", i64::from(o.quantity)),
                ("side", i64::from(o.side)),
                ("order_type", i64::from(o.order_type)),
                ("time_in_force", i64::from(o.time_in_force)),
                ("stop_price", o.stop_price),
                ("display_quantity", i64::from(o.display_quantity)),
                ("stp_mode", i64::from(o.stp_mode)),
            ] {
                m.insert(k.into(), integer(v));
            }
        }
        Input::Cancel(o) => {
            for (k, v) in [
                ("kind", "cancel"),
                ("order_id", o.order_id.as_str()),
                ("owner", o.sender.as_str()),
                ("client_order_id", o.client_cancel_id.as_str()),
            ] {
                str_field(k, v);
            }
        }
        Input::Replace(o) => {
            for (k, v) in [
                ("kind", "replace"),
                ("order_id", o.order_id.as_str()),
                ("new_order_id", o.new_order_id.as_str()),
                ("owner", o.sender.as_str()),
                ("client_order_id", o.client_replace_id.as_str()),
            ] {
                str_field(k, v);
            }
            for (k, v) in [
                ("new_price", o.new_price),
                ("new_quantity", i64::from(o.new_quantity)),
                ("new_stop_price", o.new_stop_price),
                ("new_time_in_force", i64::from(o.new_time_in_force)),
            ] {
                m.insert(k.into(), integer(v));
            }
            m.insert(
                "new_display_quantity".into(),
                o.new_display_quantity
                    .map_or(Json::Null, |n| integer(i64::from(n))),
            );
        }
        Input::MassCancel(o) => {
            str_field("kind", "mass_cancel");
            str_field("owner", &o.sender);
            str_field("client_order_id", &o.client_request_id);
            m.insert("side".into(), integer(i64::from(o.side)));
            m.insert("reason".into(), integer(i64::from(o.reason)));
        }
        Input::Tick => str_field("kind", "tick"),
        Input::Subscribe => str_field("kind", "subscribe"),
        Input::SessionControl(o) => {
            str_field("kind", "session_control");
            m.insert(
                "opening_call_seconds".into(),
                Json::number(u64::from(o.opening_call_seconds)),
            );
        }
    }
    let (_, bytes) = input.encode();
    m.insert("protobuf_hex".into(), Json::string(hex(&bytes)));
    Json::Object(m)
}
#[derive(Default)]
pub(crate) struct Collector {
    pub rows: Vec<Json>,
    command_time: Option<Time>,
    phases: BTreeMap<String, i32>,
    identities: BTreeMap<(String, String), (String, String)>,
}
impl Collector {
    #[cfg(test)]
    pub(super) fn identity_count(&self) -> usize {
        self.identities.len()
    }

    #[allow(clippy::too_many_arguments)]
    fn row(
        &self,
        seq: u64,
        index: u64,
        symbol: &str,
        kind: &str,
        id: &str,
        data: Json,
        canonical: Option<Vec<u8>>,
    ) -> Json {
        let (owner, client) = self
            .identities
            .get(&(symbol.into(), id.into()))
            .cloned()
            .unwrap_or_default();
        Json::object([
            ("global_seq", Json::number(seq)),
            (
                "command_time",
                self.command_time.map_or(Json::Null, |t| {
                    Json::object([
                        ("seconds", integer(t.seconds)),
                        ("nanos", integer(i64::from(t.nanos))),
                    ])
                }),
            ),
            ("event_index", Json::number(index)),
            ("symbol", Json::string(symbol)),
            ("kind", Json::string(kind)),
            ("order_id", Json::string(id)),
            ("owner", Json::string(owner)),
            ("client_order_id", Json::string(client)),
            ("data", data),
            (
                "canonical_hex",
                canonical.map_or(Json::Null, |b| Json::string(hex(&b))),
            ),
        ])
    }
    pub fn capture<R>(
        &mut self,
        seq: u64,
        symbol: &str,
        at: Time,
        input: &Input,
        outputs: &[Output<R>],
        diagnostics: Vec<Diagnostic>,
    ) {
        self.command_time = Some(at);
        let cmd = command(input);
        let id = text(&cmd, "order_id");
        let owner = text(&cmd, "owner");
        let client = text(&cmd, "client_order_id");
        let candidate = if matches!(input, Input::Replace(_)) {
            text(&cmd, "new_order_id")
        } else {
            id
        };
        let replace_at = if matches!(input, Input::Replace(_)) {
            outputs.iter().position(|o|matches!(o,Output::ExecutionReport(_,r) if r.order_id==id && r.execution_type==fxp::ExecutionType::ExecReplaced as i32))
        } else {
            None
        };
        let mut received = self.row(seq, 0, symbol, "command_received", id, cmd.clone(), None);
        if let Json::Object(m) = &mut received {
            m.insert("owner".into(), Json::string(owner));
            m.insert("client_order_id".into(), Json::string(client));
        }
        self.rows.push(received);
        let mut points = BTreeMap::<usize, Vec<Diagnostic>>::new();
        for d in diagnostics {
            points.entry(d.output_index).or_default().push(d);
        }
        let mut index = 1;
        for i in 0..=outputs.len() {
            if replace_at == Some(i) {
                self.identities.insert(
                    (symbol.into(), candidate.into()),
                    (owner.into(), client.into()),
                );
            }
            let ds = points.remove(&i).unwrap_or_default();
            if ds
                .iter()
                .any(|d| d.kind == "order_accepted" && d.order_id == candidate)
            {
                self.identities.insert(
                    (symbol.into(), candidate.into()),
                    (owner.into(), client.into()),
                );
            }
            for d in ds
                .iter()
                .filter(|d| !matches!(d.kind, "fill_context" | "cancel_context" | "stp_context"))
            {
                if d.kind=="order_accepted" && outputs.iter().any(|o|matches!(o,Output::ExecutionReport(_,r) if r.order_id==d.order_id && r.execution_type==fxp::ExecutionType::ExecNew as i32)) {continue;}
                let mut data = Json::object([("cause", Json::string(&d.cause))]);
                if let Json::Object(m) = &mut data {
                    if d.kind == "iceberg_refresh" {
                        m.insert("quantity".into(), Json::number(u64::from(d.quantity)));
                    }
                    if d.kind == "order_accepted" {
                        m.insert("order_quantity".into(), Json::number(u64::from(d.quantity)));
                        m.insert(
                            "leaves_quantity".into(),
                            Json::number(u64::from(d.quantity)),
                        );
                    }
                }
                self.rows.push(self.row(
                    seq,
                    index,
                    symbol,
                    if d.kind == "order_accepted" {
                        "accepted"
                    } else {
                        d.kind
                    },
                    &d.order_id,
                    data,
                    None,
                ));
                index += 1;
            }
            let Some(output) = outputs.get(i) else {
                continue;
            };
            let Some(canonical) = canonical_event(output) else {
                continue;
            };
            let mut id = String::new();
            let (kind, data) = match output {
                Output::ExecutionReport(_, r) => {
                    id = r.order_id.clone();
                    let kind = match fxp::ExecutionType::try_from(r.execution_type) {
                        Ok(fxp::ExecutionType::ExecNew) => "accepted",
                        Ok(fxp::ExecutionType::ExecFilled) => "fill",
                        Ok(fxp::ExecutionType::ExecRejected) => "rejected",
                        Ok(fxp::ExecutionType::ExecReplaced) => "replaced",
                        Ok(fxp::ExecutionType::ExecExpired) => "expired",
                        Ok(fxp::ExecutionType::ExecCanceled) => "canceled",
                        Ok(fxp::ExecutionType::ExecRestated) => "restated",
                        _ => "execution_report",
                    };
                    let cause = if !r.reject_reason.is_empty() {
                        r.reject_reason.clone()
                    } else if kind == "expired" {
                        "expiry".into()
                    } else if let Some(d) = ds
                        .iter()
                        .find(|d| d.kind == "cancel_context" && d.order_id == id)
                    {
                        d.cause.clone()
                    } else if kind == "canceled"
                        && matches!(input,Input::Cancel(c) if c.order_id==id)
                    {
                        "user".into()
                    } else if kind == "canceled" {
                        "cause not recorded".into()
                    } else {
                        String::new()
                    };
                    let context = ds
                        .iter()
                        .find(|d| d.kind == "fill_context" && d.order_id == id);
                    (
                        kind,
                        Json::object([
                            ("execution_id", Json::string(&r.execution_id)),
                            ("orig_order_id", Json::string(&r.orig_order_id)),
                            (
                                "new_order_id",
                                Json::string(if kind == "replaced" {
                                    text(&cmd, "new_order_id")
                                } else {
                                    ""
                                }),
                            ),
                            ("client_order_id", Json::string(&r.client_order_id)),
                            ("price", integer(r.execution_price)),
                            ("quantity", integer(i64::from(r.executed_quantity))),
                            ("leaves_quantity", integer(i64::from(r.leaves_quantity))),
                            ("order_quantity", integer(i64::from(r.order_quantity))),
                            ("cause", Json::string(cause)),
                            (
                                "role",
                                Json::string(context.map_or(
                                    if r.auction { "auction" } else { "not recorded" },
                                    |d| d.role,
                                )),
                            ),
                            (
                                "counterparty",
                                Json::string(context.map_or("", |d| d.counterparty.as_str())),
                            ),
                            ("auction", Json::Bool(r.auction)),
                        ]),
                    )
                }
                Output::Trade(t) => (
                    "trade",
                    Json::object([
                        ("trade_id", Json::string(&t.trade_id)),
                        ("price", integer(t.trade_price)),
                        ("quantity", integer(i64::from(t.trade_quantity))),
                        ("auction", Json::Bool(t.is_auction)),
                    ]),
                ),
                Output::TradingSessionStatus(t) => (
                    "phase",
                    Json::object([("phase", integer(i64::from(t.phase)))]),
                ),
                Output::AuctionInfo(t) => (
                    "auction",
                    Json::object([
                        ("price", integer(t.indicative_price)),
                        ("quantity", Json::number(t.indicative_volume)),
                    ]),
                ),
                _ => unreachable!(),
            };
            let business_time = match output {
                Output::ExecutionReport(_, m) => m.timestamp,
                Output::Trade(m) => m.timestamp,
                Output::TradingSessionStatus(m) => m.timestamp,
                Output::AuctionInfo(m) => m.timestamp,
                _ => None,
            };
            if let Output::TradingSessionStatus(t) = output {
                self.phases.insert(symbol.into(), t.phase);
            }
            let mut data = data;
            if let Json::Object(m) = &mut data {
                if kind == "accepted"
                    && let Some(phase) = self.phases.get(symbol)
                {
                    m.insert("session_phase".into(), integer(i64::from(*phase)));
                }
                if let Some(d) = ds
                    .iter()
                    .find(|d| d.kind == "stp_context" && d.order_id == id)
                {
                    m.insert("opposing_order".into(), Json::string(&d.counterparty));
                }
                m.insert(
                    "business_time".into(),
                    business_time.map_or(Json::Null, |t| {
                        Json::object([
                            ("seconds", integer(t.seconds)),
                            ("nanos", integer(i64::from(t.nanos))),
                        ])
                    }),
                );
            }
            let mut row = self.row(seq, index, symbol, kind, &id, data, Some(canonical));
            if let (Output::ExecutionReport(_, r), Json::Object(m)) = (output, &mut row) {
                m.insert("client_order_id".into(), Json::string(&r.client_order_id));
                if kind == "rejected" {
                    m.insert("owner".into(), Json::string(owner));
                }
            }
            self.rows.push(row);
            index += 1;
        }
        // Keep only live identities in the recording process. Stored rows already
        // own historical metadata. Decide after the entire command so expiry /
        // replacement followed by reuse of the same ID preserves the new owner.
        let mut terminal = BTreeMap::new();
        for output in outputs {
            if let Output::ExecutionReport(_, r) = output
                && r.execution_type != fxp::ExecutionType::ExecRejected as i32
            {
                terminal.insert(
                    r.order_id.as_str(),
                    matches!(
                        fxp::OrderStatus::try_from(r.order_status),
                        Ok(fxp::OrderStatus::OrderFilled
                            | fxp::OrderStatus::OrderCanceled
                            | fxp::OrderStatus::OrderExpired
                            | fxp::OrderStatus::OrderReplaced)
                    ),
                );
            }
        }
        for (id, ended) in terminal {
            if ended {
                self.identities.remove(&(symbol.into(), id.into()));
            }
        }
    }
}
