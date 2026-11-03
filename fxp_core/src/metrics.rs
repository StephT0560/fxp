// SPDX-License-Identifier: MIT
//! Opt-in fixed-cardinality counters. No engine rules or per-user/order labels.
use crate::fxp::{self, fxp_message::Payload};
use std::fmt::Write as _;
use std::io;
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering::Relaxed};
use std::sync::{Arc, OnceLock};
use std::time::Instant;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

static ENABLED: AtomicBool = AtomicBool::new(false);
static START: OnceLock<Instant> = OnceLock::new();
static CLIENTS: AtomicI64 = AtomicI64::new(0);
static IN: [AtomicU64; 24] = [const { AtomicU64::new(0) }; 24];
static OUT: [AtomicU64; 24] = [const { AtomicU64::new(0) }; 24];
static REJECTS: [AtomicU64; 8] = [const { AtomicU64::new(0) }; 8];
static COMMANDS: AtomicU64 = AtomicU64::new(0);
static FSYNCS: AtomicU64 = AtomicU64::new(0);
static FSYNC_ERRORS: AtomicU64 = AtomicU64::new(0);
static FSYNC_NS: AtomicU64 = AtomicU64::new(0);
const BOUNDS: [u64; 12] = [
    100_000,
    250_000,
    500_000,
    1_000_000,
    2_500_000,
    5_000_000,
    10_000_000,
    25_000_000,
    50_000_000,
    100_000_000,
    250_000_000,
    1_000_000_000,
];
static BUCKETS: [AtomicU64; 13] = [const { AtomicU64::new(0) }; 13];
const TYPES: [&str; 24] = [
    "unknown",
    "heartbeat",
    "logon",
    "logout",
    "gap_fill",
    "new_order",
    "order_acknowledgment",
    "order_cancel",
    "execution_report",
    "market_data_snapshot",
    "market_data_incremental",
    "market_data_request",
    "trade_capture_report",
    "error_message",
    "logon_ack",
    "order_cancel_replace",
    "trading_session_status",
    "auction_info",
    "order_mass_cancel",
    "order_mass_cancel_report",
    "order_status_request",
    "order_status_response",
    "session_control",
    "session_control_report",
];
const REASONS: [&str; 8] = [
    "authentication",
    "permission",
    "rate_limit",
    "capacity",
    "unknown_order",
    "duplicate",
    "invalid",
    "other",
];
pub(crate) fn enabled() -> bool {
    ENABLED.load(Relaxed)
}
pub(crate) struct Client(bool);
impl Drop for Client {
    fn drop(&mut self) {
        if self.0 {
            CLIENTS.fetch_sub(1, Relaxed);
        }
    }
}
pub(crate) fn client() -> Client {
    let active = enabled();
    if active {
        CLIENTS.fetch_add(1, Relaxed);
    }
    Client(active)
}
pub(crate) fn frame(raw: &[u8], incoming: bool) {
    if !enabled() {
        return;
    }
    // FXPMessage contains only a oneof with field numbers 1..=23. Locally
    // encoded frames start with that tag. Unknown/malformed types share one slot.
    let kind = message_kind(raw);
    (if incoming { &IN } else { &OUT })[kind].fetch_add(1, Relaxed);
}
fn message_kind(raw: &[u8]) -> usize {
    let kind = match raw {
        [a, ..] if *a < 128 && a & 7 == 2 => usize::from(a >> 3),
        [a, b, ..] if a & 7 == 2 && *b == 1 => usize::from((a & 0x7f) >> 3) + 16,
        _ => 0,
    };
    if kind < TYPES.len() { kind } else { 0 }
}
pub(crate) fn command() {
    if enabled() {
        COMMANDS.fetch_add(1, Relaxed);
    }
}
pub(crate) fn reject(reason: &str) {
    if !enabled() {
        return;
    }
    let r = reason.to_ascii_lowercase();
    let index = if ["token", "logon", "auth", "session expired"]
        .iter()
        .any(|s| r.contains(s))
    {
        0
    } else if ["permission", "operator", "owner", "not owned"]
        .iter()
        .any(|s| r.contains(s))
    {
        1
    } else if r.contains("rate limit") {
        2
    } else if ["limit", "maximum", "unavailable", "queue"]
        .iter()
        .any(|s| r.contains(s))
    {
        3
    } else if ["unknown order", "not found"].iter().any(|s| r.contains(s)) {
        4
    } else if r.contains("duplicate") {
        5
    } else if ["invalid", "required", "quantity", "price", "unknown"]
        .iter()
        .any(|s| r.contains(s))
    {
        6
    } else {
        7
    };
    REJECTS[index].fetch_add(1, Relaxed);
}
pub(crate) fn response(msg: &fxp::FxpMessage) {
    if !enabled() {
        return;
    }
    let reason = match &msg.payload {
        Some(Payload::ExecutionReport(r)) if r.order_status == 6 => {
            if r.reject_reason.is_empty() {
                "rejected"
            } else {
                &r.reject_reason
            }
        }
        Some(Payload::ErrorMessage(r)) => &r.error_message,
        Some(Payload::LogonAck(r)) if !r.accepted => "authentication",
        Some(Payload::OrderMassCancelReport(r)) => &r.reject_reason,
        Some(Payload::OrderStatusResponse(r)) => &r.reject_reason,
        Some(Payload::SessionControlReport(r)) => &r.reject_reason,
        _ => "",
    };
    if !reason.is_empty() {
        reject(reason);
    }
}
pub(crate) fn fsync_start() -> Option<Instant> {
    enabled().then(Instant::now)
}
pub(crate) fn fsync_done(start: Option<Instant>, success: bool) {
    if let Some(start) = start {
        let ns = start.elapsed().as_nanos().min(u128::from(u64::MAX)) as u64;
        FSYNCS.fetch_add(1, Relaxed);
        FSYNC_NS.fetch_add(ns, Relaxed);
        if !success {
            FSYNC_ERRORS.fetch_add(1, Relaxed);
        }
        let index = BOUNDS.iter().position(|b| ns <= *b).unwrap_or(BOUNDS.len());
        BUCKETS[index].fetch_add(1, Relaxed);
    }
}
fn render() -> String {
    let mut b = String::new();
    macro_rules! metric {
        ($name:literal,$typ:literal,$help:literal) => {
            writeln!(
                b,
                concat!("# HELP ", $name, " ", $help, "\n# TYPE ", $name, " ", $typ)
            )
            .unwrap()
        };
    }
    metric!(
        "fxp_connected_clients",
        "gauge",
        "Current core transport connections including authentication."
    );
    writeln!(b, "fxp_connected_clients {}", CLIENTS.load(Relaxed)).unwrap();
    metric!(
        "fxp_messages_total",
        "counter",
        "Wire messages received or successfully sent by fixed protocol type."
    );
    for (i, t) in TYPES.iter().enumerate() {
        writeln!(b,"fxp_messages_total{{direction=\"in\",type=\"{t}\"}} {}\nfxp_messages_total{{direction=\"out\",type=\"{t}\"}} {}",IN[i].load(Relaxed),OUT[i].load(Relaxed)).unwrap();
    }
    metric!(
        "fxp_rejects_total",
        "counter",
        "Rejections generated classified into fixed reason categories."
    );
    for (i, r) in REASONS.iter().enumerate() {
        writeln!(
            b,
            "fxp_rejects_total{{reason=\"{r}\"}} {}",
            REJECTS[i].load(Relaxed)
        )
        .unwrap();
    }
    metric!(
        "fxp_commands_processed_total",
        "counter",
        "Symbol-task operations completed including ticks queries and subscriptions."
    );
    writeln!(b, "fxp_commands_processed_total {}", COMMANDS.load(Relaxed)).unwrap();
    metric!(
        "fxp_journal_fsync_total",
        "counter",
        "Journal sync_data calls attempted excluding startup and bundle finalization."
    );
    writeln!(b, "fxp_journal_fsync_total {}", FSYNCS.load(Relaxed)).unwrap();
    metric!(
        "fxp_journal_fsync_errors_total",
        "counter",
        "Failed journal sync_data calls."
    );
    writeln!(
        b,
        "fxp_journal_fsync_errors_total {}",
        FSYNC_ERRORS.load(Relaxed)
    )
    .unwrap();
    metric!(
        "fxp_journal_fsync_duration_seconds",
        "histogram",
        "Journal sync_data latency including failed calls."
    );
    let mut cumulative = 0;
    for (i, upper) in BOUNDS.iter().enumerate() {
        cumulative += BUCKETS[i].load(Relaxed);
        writeln!(
            b,
            "fxp_journal_fsync_duration_seconds_bucket{{le=\"{}\"}} {cumulative}",
            *upper as f64 / 1e9
        )
        .unwrap();
    }
    cumulative += BUCKETS[BOUNDS.len()].load(Relaxed);
    writeln!(b,"fxp_journal_fsync_duration_seconds_bucket{{le=\"+Inf\"}} {cumulative}\nfxp_journal_fsync_duration_seconds_count {cumulative}\nfxp_journal_fsync_duration_seconds_sum {}",FSYNC_NS.load(Relaxed) as f64/1e9).unwrap();
    metric!(
        "fxp_process_uptime_seconds",
        "gauge",
        "Seconds since process metrics initialization."
    );
    writeln!(
        b,
        "fxp_process_uptime_seconds {}",
        START.get().map_or(0.0, |s| s.elapsed().as_secs_f64())
    )
    .unwrap();
    metric!("fxp_build_info", "gauge", "Build version and Git revision.");
    // Debug escaping is not Prometheus escaping for arbitrary control characters.
    let escape = |s: &str| {
        s.replace('\\', "\\\\")
            .replace('\n', "\\n")
            .replace('"', "\\\"")
            .replace('\r', "")
    };
    writeln!(
        b,
        "fxp_build_info{{version=\"{}\",git_sha=\"{}\"}} 1",
        escape(env!("CARGO_PKG_VERSION")),
        escape(env!("FXP_BUILD_GIT_SHA"))
    )
    .unwrap();
    b
}
pub(crate) struct Server(tokio::task::JoinHandle<()>);
impl Drop for Server {
    fn drop(&mut self) {
        self.0.abort();
    }
}
/// Empty/unset disables. Port-only and :port bind IPv4 loopback; public IPs require an explicit address.
pub(crate) async fn start() -> io::Result<Option<Server>> {
    let Some(value) = std::env::var_os("FXP_CORE_METRICS_ADDR") else {
        return Ok(None);
    };
    let value = value
        .into_string()
        .map_err(|_| io::Error::other("invalid metrics address"))?;
    if value.is_empty() {
        return Ok(None);
    }
    let address = if let Ok(port) = value.trim_start_matches(':').parse::<u16>() {
        std::net::SocketAddr::from(([127, 0, 0, 1], port))
    } else {
        value
            .parse()
            .map_err(|_| io::Error::other("metrics address must be an IP literal and port"))?
    };
    let listener = tokio::net::TcpListener::bind(address).await?;
    START.get_or_init(Instant::now);
    ENABLED.store(true, Relaxed);
    let slots = Arc::new(tokio::sync::Semaphore::new(32));
    Ok(Some(Server(tokio::spawn(async move {
        loop {
            let Ok((mut socket, _)) = listener.accept().await else {
                break;
            };
            let Ok(slot) = slots.clone().try_acquire_owned() else {
                continue;
            };
            tokio::spawn(async move {
                let _slot = slot;
                let _ = tokio::time::timeout(std::time::Duration::from_secs(2),async {
 let mut request = Vec::new();
 let mut chunk = [0; 1024];
 while !request.windows(4).any(|w| w == b"\r\n\r\n") {
 let n = socket.read(&mut chunk).await?;
 if n == 0 || request.len()+n > 8192 { return Ok::<_,io::Error>(()); }
 request.extend_from_slice(&chunk[..n]);
 }
 let (status,body) = if request.starts_with(b"GET /metrics HTTP/1.") { ("200 OK",render()) } else { ("404 Not Found",String::new()) };
 let response = format!("HTTP/1.1 {status}\r\nContent-Type: text/plain; version=0.0.4; charset=utf-8\r\nCache-Control: no-store\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",body.len());
 socket.write_all(response.as_bytes()).await
 }).await;
            });
        }
    }))))
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn multi_byte_protocol_tags_are_classified_without_dynamic_labels() {
        use prost::Message;
        let msg = fxp::FxpMessage {
            payload: Some(Payload::SessionControlReport(Default::default())),
        };
        assert_eq!(message_kind(&msg.encode_to_vec()), 23);
        let msg = fxp::FxpMessage {
            payload: Some(Payload::TradingSessionStatus(Default::default())),
        };
        assert_eq!(message_kind(&msg.encode_to_vec()), 16);
        for raw in [&[][..], &[255], &[255, 255], &[0]] {
            assert_eq!(message_kind(raw), 0);
        }
    }
}
