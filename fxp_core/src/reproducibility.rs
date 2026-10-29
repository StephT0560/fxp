// SPDX-License-Identifier: MIT
//! Canonical engine business events and recorded run bundles (no CLI).
//! See `docs/reproducibility.md` for the byte format and guarantee.
mod json;
use crate::engine::{Output, SessionSchedule};
use crate::order_book::order::StpMode;
use crate::symbol_config::SymbolConfig;
use json::Json;
use prost::Message;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};

pub const BUNDLE_VERSION: &str = "fxp-run-v1";
pub const CANONICAL_VERSION: &str = "fxp-business-v1";
pub const STATE_VERSION: &str = "fxp-state-v1";
const SCHEMA: &[u8] = include_bytes!("../proto/fxp.proto");
fn invalid(error: impl std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error.to_string())
}
pub fn sha256(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}
fn hash_file(path: &Path) -> io::Result<String> {
    let mut f = File::open(path)?;
    let mut h = Sha256::new();
    let mut buf = [0; 65536];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(format!("{:x}", h.finalize()))
}

/// Engine-generated timestamps are business time. Headers and routing handles
/// are excluded. Protobuf schemas contain no maps, floats or unknown fields.
/// A frame is a one-byte event tag, u64 big-endian payload length, then the
/// normalized protobuf payload encoded in field-number order by prost.
pub fn canonical_event<R>(event: &Output<R>) -> Option<Vec<u8>> {
    let (tag, payload) = match event {
        Output::ExecutionReport(_, m) => {
            let mut m = m.clone();
            m.header = None;
            (1, m.encode_to_vec())
        }
        Output::Trade(m) => {
            let mut m = m.clone();
            m.header = None;
            (2, m.encode_to_vec())
        }
        Output::TradingSessionStatus(m) => (3, m.encode_to_vec()),
        Output::AuctionInfo(m) => (4, m.encode_to_vec()),
        _ => return None,
    };
    let mut out = vec![tag];
    out.extend_from_slice(&(payload.len() as u64).to_be_bytes());
    out.extend(payload);
    Some(out)
}
#[derive(Clone)]
struct Stream {
    count: u64,
    hasher: Sha256,
}
impl Default for Stream {
    fn default() -> Self {
        let mut hasher = Sha256::new();
        hasher.update(CANONICAL_VERSION.as_bytes());
        hasher.update([0]);
        Self { count: 0, hasher }
    }
}
impl Stream {
    fn push(&mut self, bytes: &[u8]) {
        self.count += 1;
        self.hasher.update(bytes);
    }
    fn result(&self) -> EventDigest {
        EventDigest {
            count: self.count,
            sha256: format!("{:x}", self.hasher.clone().finalize()),
        }
    }
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EventDigest {
    pub count: u64,
    pub sha256: String,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RunSummary {
    pub global: EventDigest,
    pub symbols: BTreeMap<String, EventDigest>,
    pub final_state_sha256: String,
}
impl RunSummary {
    fn json(&self) -> Json {
        fn digest(d: &EventDigest) -> Json {
            Json::object([
                ("count", Json::number(d.count)),
                ("sha256", Json::string(&d.sha256)),
            ])
        }
        Json::object([
            ("canonical_version", Json::string(CANONICAL_VERSION)),
            ("state_version", Json::string(STATE_VERSION)),
            ("global", digest(&self.global)),
            (
                "symbols",
                Json::object(self.symbols.iter().map(|(k, v)| (k, digest(v)))),
            ),
            ("final_state_sha256", Json::string(&self.final_state_sha256)),
        ])
    }
}
#[derive(Default)]
pub(crate) struct BusinessDigest {
    global: Stream,
    symbols: BTreeMap<String, Stream>,
}
impl BusinessDigest {
    pub(crate) fn observe<R>(&mut self, symbol: &str, outputs: &[Output<R>]) {
        let stream = self.symbols.entry(symbol.into()).or_default();
        for output in outputs {
            if let Some(bytes) = canonical_event(output) {
                self.global.push(&bytes);
                stream.push(&bytes);
            }
        }
    }
    pub(crate) fn summary(&self, state: &[u8]) -> RunSummary {
        RunSummary {
            global: self.global.result(),
            symbols: self
                .symbols
                .iter()
                .map(|(k, v)| (k.clone(), v.result()))
                .collect(),
            final_state_sha256: sha256(state),
        }
    }
}

/// Explicit fixed-width state encoding. Variable-length values have u64 lengths.
#[derive(Default)]
pub(crate) struct StateWriter(pub(crate) Vec<u8>);
impl StateWriter {
    pub(crate) fn u64(&mut self, n: u64) {
        self.0.extend_from_slice(&n.to_be_bytes());
    }
    pub(crate) fn i64(&mut self, n: i64) {
        self.0.extend_from_slice(&n.to_be_bytes());
    }
    pub(crate) fn i128(&mut self, n: i128) {
        self.0.extend_from_slice(&n.to_be_bytes());
    }
    pub(crate) fn bytes(&mut self, b: &[u8]) {
        self.u64(b.len() as u64);
        self.0.extend_from_slice(b);
    }
    pub(crate) fn text(&mut self, s: &str) {
        self.bytes(s.as_bytes());
    }
}

/// Offline replay result. An incomplete bundle is replayable but never verified.
#[derive(Debug)]
pub struct ReplayResult {
    pub verified: bool,
    pub summary: RunSummary,
    pub final_state: Vec<u8>,
}
/// Replays read-only; does not truncate a torn tail or append catch-up ticks.
pub fn replay_bundle(dir: impl AsRef<Path>) -> io::Result<ReplayResult> {
    let dir = dir.as_ref();
    let manifest = Json::parse(&fs::read_to_string(dir.join("manifest.json"))?)?;
    require(&manifest, "bundle_format", BUNDLE_VERSION)?;
    require(
        &manifest,
        "replay_version",
        crate::event_log::REPLAY_VERSION,
    )?;
    require(&manifest, "canonical_version", CANONICAL_VERSION)?;
    require(&manifest, "state_version", STATE_VERSION)?;
    require(&manifest, "proto_schema_sha256", &sha256(SCHEMA))?;
    let symbols = fs::read_to_string(dir.join("symbols.csv"))?;
    require(
        &manifest,
        "symbol_config_sha256",
        &sha256(symbols.as_bytes()),
    )?;
    let config = SymbolConfig::parse(&symbols).map_err(invalid)?;
    let schedule = match manifest.get("session_schedule")? {
        Json::Null => None,
        j => Some(j.text()?.parse::<SessionSchedule>().map_err(invalid)?),
    };
    let stp = StpMode::parse(manifest.get("stp_default")?.text()?).map_err(invalid)?;
    let complete = match manifest.get("complete")? {
        Json::Bool(b) => *b,
        _ => return Err(invalid("invalid complete marker")),
    };
    let log = dir.join("commands.log");
    if complete {
        require(&manifest, "commands_sha256", &hash_file(&log)?)?;
    }
    let journal =
        crate::event_log::Journal::replay_readonly(&log, schedule, stp, (), config, complete)?;
    let final_state = journal.canonical_state();
    let summary = journal.summary();
    if complete {
        let expected = Json::parse(&fs::read_to_string(dir.join("expected.json"))?)?;
        if expected != summary.json() {
            return Err(invalid(
                "bundle verification failed: business events or final state differ",
            ));
        }
    }
    Ok(ReplayResult {
        verified: complete,
        summary,
        final_state,
    })
}
fn require(j: &Json, key: &str, expected: &str) -> io::Result<()> {
    if j.get(key)?.text()? != expected {
        return Err(invalid(format!(
            "bundle {key} mismatch: expected {expected}"
        )));
    }
    Ok(())
}

pub(crate) struct Recording {
    dir: PathBuf,
    manifest: Json,
    _lock: File,
    finished: bool,
}
impl Recording {
    pub(crate) fn create(
        dir: &Path,
        schedule: Option<SessionSchedule>,
        stp: StpMode,
        config: &SymbolConfig,
        meta: &str,
    ) -> io::Result<Self> {
        if meta.len() > 4 * 1024 * 1024 {
            return Err(invalid("FXP_RUN_META_JSON exceeds 4 MiB size limit"));
        }
        let meta = Json::parse(meta)?;
        if !matches!(meta, Json::Object(_)) {
            return Err(invalid("FXP_RUN_META_JSON must be a JSON object"));
        }
        // Never overwrite an existing run, even an incomplete one.
        let mut builder = fs::DirBuilder::new();
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            builder.mode(0o700);
        }
        builder.create(dir)?;
        let lock = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(dir.join(".recording.lock"))?;
        lock.try_lock().map_err(io::Error::other)?;
        let symbols = config.canonical();
        let manifest = Json::object([
            ("bundle_format", Json::string(BUNDLE_VERSION)),
            ("canonical_version", Json::string(CANONICAL_VERSION)),
            ("state_version", Json::string(STATE_VERSION)),
            ("fxp_version", Json::string(env!("CARGO_PKG_VERSION"))),
            ("git_sha", Json::string(env!("FXP_BUILD_GIT_SHA"))),
            ("proto_schema_sha256", Json::string(sha256(SCHEMA))),
            (
                "replay_version",
                Json::string(crate::event_log::REPLAY_VERSION),
            ),
            ("symbol_config_file", Json::string("symbols.csv")),
            (
                "symbol_config_sha256",
                Json::string(sha256(symbols.as_bytes())),
            ),
            (
                "session_schedule",
                schedule.map_or(Json::Null, |s| Json::string(s.to_string())),
            ),
            ("stp_default", Json::string(stp.as_str())),
            ("meta", meta),
            ("start_time", Json::string(wall_time())),
            ("end_time", Json::Null),
            ("complete", Json::Bool(false)),
            ("commands_sha256", Json::Null),
        ]);
        Json::parse(&manifest.encode())?;
        atomic_write(dir, "symbols.csv", symbols.as_bytes())?;
        atomic_write(dir, "manifest.json", manifest.encode().as_bytes())?;
        Ok(Self {
            dir: dir.into(),
            manifest,
            _lock: lock,
            finished: false,
        })
    }
    pub(crate) fn is_finished(&self) -> bool {
        self.finished
    }
    pub(crate) fn log_path(&self) -> PathBuf {
        self.dir.join("commands.log")
    }
    pub(crate) fn finish(&mut self, summary: &RunSummary) -> io::Result<()> {
        if self.finished {
            return Ok(());
        }
        let hash = hash_file(&self.log_path())?;
        atomic_write(
            &self.dir,
            "expected.json",
            summary.json().encode().as_bytes(),
        )?;
        let Json::Object(fields) = &mut self.manifest else {
            unreachable!()
        };
        fields.insert("commands_sha256".into(), Json::string(hash));
        fields.insert("end_time".into(), Json::string(wall_time()));
        fields.insert("complete".into(), Json::Bool(true));
        // Publish completion last. A crash before this rename is unverified.
        atomic_write(
            &self.dir,
            "manifest.json",
            self.manifest.encode().as_bytes(),
        )?;
        self.finished = true;
        Ok(())
    }
}
fn atomic_write(dir: &Path, name: &str, bytes: &[u8]) -> io::Result<()> {
    let tmp = dir.join(format!(".{name}.tmp"));
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut f = options.open(&tmp)?;
    f.write_all(bytes)?;
    f.sync_all()?;
    fs::rename(tmp, dir.join(name))?;
    File::open(dir)?.sync_all()
}
fn wall_time() -> String {
    // Bundle metadata only, never read by the engine or digest.
    let t = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default();
    format!("{}.{:09}", t.as_secs(), t.subsec_nanos())
}
#[cfg(test)]
mod tests;

#[cfg(test)]
mod vectors;
