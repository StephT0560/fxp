// SPDX-License-Identifier: MIT
//! Opt-in synchronous, globally ordered input persistence.
use crate::engine::{
    CancelMsg, CancelReplaceMsg, Clock, Engine, ExecIds, MassCancelMsg, Output, SessionSchedule,
    Time,
};
use crate::fxp;
use crate::order_book::order::StpMode;
use prost::Message;
use std::collections::HashMap;
use std::fs::{File, OpenOptions};
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

const MAGIC: &[u8; 8] = b"FXPEVL01";
const MAX_RECORD: usize = 4 * 1024 * 1024;
pub(crate) const REPLAY_VERSION: &str = "engine-input-v1";

#[derive(Clone, PartialEq, Message)]
struct Record {
    #[prost(uint64, tag = "1")]
    seq: u64,
    #[prost(int64, tag = "2")]
    seconds: i64,
    #[prost(int32, tag = "3")]
    nanos: i32,
    #[prost(string, tag = "4")]
    symbol: String,
    #[prost(uint32, tag = "5")]
    kind: u32,
    #[prost(bytes = "vec", tag = "6")]
    payload: Vec<u8>,
}

#[derive(Clone)]
pub(crate) enum Input {
    New(fxp::NewOrderSingle),
    Cancel(fxp::OrderCancelRequest),
    Replace(fxp::OrderCancelReplaceRequest),
    Tick,
    Subscribe,
    MassCancel(fxp::OrderMassCancelRequest),
    SessionControl(fxp::SessionControl),
}

impl Input {
    pub(crate) fn encode(&self) -> (u32, Vec<u8>) {
        match self {
            Self::New(m) => (1, m.encode_to_vec()),
            Self::Cancel(m) => (2, m.encode_to_vec()),
            Self::Replace(m) => (3, m.encode_to_vec()),
            Self::Tick => (4, Vec::new()),
            Self::Subscribe => (5, Vec::new()),
            Self::MassCancel(m) => (6, m.encode_to_vec()),
            Self::SessionControl(m) => (7, m.encode_to_vec()),
        }
    }

    fn decode(record: &Record) -> io::Result<Self> {
        let (input, symbol) = match record.kind {
            1 => {
                let m = fxp::NewOrderSingle::decode(record.payload.as_slice()).map_err(invalid)?;
                let symbol = m.symbol.clone();
                (Self::New(m), symbol)
            }
            2 => {
                let m =
                    fxp::OrderCancelRequest::decode(record.payload.as_slice()).map_err(invalid)?;
                let symbol = m.symbol.clone();
                (Self::Cancel(m), symbol)
            }
            3 => {
                let m = fxp::OrderCancelReplaceRequest::decode(record.payload.as_slice())
                    .map_err(invalid)?;
                let symbol = m.symbol.clone();
                (Self::Replace(m), symbol)
            }
            6 => {
                let m = fxp::OrderMassCancelRequest::decode(record.payload.as_slice())
                    .map_err(invalid)?;
                let symbol = m.symbol.clone();
                (Self::MassCancel(m), symbol)
            }
            7 => {
                let m = fxp::SessionControl::decode(record.payload.as_slice()).map_err(invalid)?;
                (Self::SessionControl(m), record.symbol.clone())
            }
            4 if record.payload.is_empty() => (Self::Tick, record.symbol.clone()),
            5 if record.payload.is_empty() => (Self::Subscribe, record.symbol.clone()),
            _ => return Err(invalid("unsupported event kind or payload")),
        };
        if symbol != record.symbol || !(0..1_000_000_000).contains(&record.nanos) {
            return Err(invalid("invalid event symbol or clock"));
        }
        Ok(input)
    }
}

fn invalid(error: impl std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error.to_string())
}

fn crc32(bytes: &[u8]) -> u32 {
    let mut crc = !0_u32;
    for byte in bytes {
        crc ^= u32::from(*byte);
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xedb8_8320 & 0_u32.wrapping_sub(crc & 1));
        }
    }
    !crc
}

fn frame(record: &Record) -> io::Result<Vec<u8>> {
    let body = record.encode_to_vec();
    if body.is_empty() || body.len() > MAX_RECORD {
        return Err(invalid("event record exceeds size limit"));
    }
    let mut bytes = Vec::with_capacity(body.len() + 8);
    bytes.extend_from_slice(&(body.len() as u32).to_be_bytes());
    bytes.extend_from_slice(&body);
    bytes.extend_from_slice(&crc32(&bytes).to_be_bytes());
    Ok(bytes)
}

fn read_exact_or_tail(file: &mut File, bytes: &mut [u8]) -> io::Result<bool> {
    match file.read_exact(bytes) {
        Ok(()) => Ok(true),
        Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => Ok(false),
        Err(e) => Err(e),
    }
}

fn read_frame(file: &mut File) -> io::Result<Option<Record>> {
    let mut prefix = [0; 4];
    if !read_exact_or_tail(file, &mut prefix)? {
        return Ok(None);
    }
    let len = u32::from_be_bytes(prefix) as usize;
    if len == 0 || len > MAX_RECORD {
        return Ok(None);
    }
    let mut bytes = vec![0; len + 4];
    if !read_exact_or_tail(file, &mut bytes)? {
        return Ok(None);
    }
    let expected = u32::from_be_bytes(bytes[len..].try_into().unwrap());
    bytes.truncate(len);
    let mut checked = prefix.to_vec();
    checked.extend_from_slice(&bytes);
    if crc32(&checked) != expected {
        return Ok(None);
    }
    Ok(Record::decode(bytes.as_slice()).ok())
}

struct EventLog {
    file: File,
    next_seq: u64,
    failed: bool,
}

impl EventLog {
    #[cfg(test)]
    fn open(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
    ) -> io::Result<(Self, Vec<Record>)> {
        Self::open_configured(path, schedule, stp_default, &Default::default())
    }
    fn open_configured(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
        symbol_config: &crate::symbol_config::SymbolConfig,
    ) -> io::Result<(Self, Vec<Record>)> {
        // Replay must resolve STP_UNSPECIFIED the same way; logs written
        // without an STP default keep their original header.
        let stp = match stp_default {
            StpMode::None => String::new(),
            mode => format!(";stp_default={}", mode.as_str()),
        };
        let symbol_text = symbol_config.canonical();
        let symbol_suffix = if symbol_text.is_empty() {
            String::new()
        } else {
            format!(";symbols={symbol_text}")
        };
        let mut options = OpenOptions::new();
        options.read(true).write(true).create(true).truncate(false);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let mut file = options.open(path)?;
        file.try_lock().map_err(io::Error::other)?;
        let config = Record {
            seq: 0,
            seconds: 0,
            nanos: 0,
            symbol: String::new(),
            kind: 0,
            payload: format!("{REPLAY_VERSION};schedule={schedule:?}{stp}{symbol_suffix}")
                .into_bytes(),
        };
        if file.metadata()?.len() == 0 {
            file.write_all(MAGIC)?;
            file.write_all(&frame(&config)?)?;
            file.sync_all()?;
            let parent = path
                .parent()
                .filter(|p| !p.as_os_str().is_empty())
                .unwrap_or(Path::new("."));
            File::open(parent)?.sync_all()?;
        }
        file.seek(SeekFrom::Start(0))?;
        let mut magic = [0; 8];
        if !read_exact_or_tail(&mut file, &mut magic)? || &magic != MAGIC {
            return Err(invalid("invalid event log magic/version"));
        }
        if read_frame(&mut file)?.as_ref() != Some(&config) {
            return Err(invalid(
                "event log configuration/version mismatch or corrupt header",
            ));
        }
        let mut good = file.stream_position()?;
        let len = file.metadata()?.len();
        let mut records = Vec::new();
        let mut next_seq = 1;
        while good < len {
            let Some(record) = read_frame(&mut file)? else {
                file.set_len(good)?;
                file.sync_all()?;
                tracing::warn!(
                    offset = good,
                    discarded_bytes = len - good,
                    "truncated torn or corrupt event-log tail"
                );
                break;
            };
            if record.seq != next_seq {
                return Err(invalid("event log sequence gap"));
            }
            next_seq = next_seq
                .checked_add(1)
                .ok_or_else(|| invalid("event sequence exhausted"))?;
            records.push(record);
            good = file.stream_position()?;
        }
        file.seek(SeekFrom::Start(good))?;
        Ok((
            Self {
                file,
                next_seq,
                failed: false,
            },
            records,
        ))
    }

    fn append(&mut self, symbol: &str, at: Time, input: &Input) -> io::Result<()> {
        if self.failed {
            return Err(io::Error::other("event log is failed; restart required"));
        }
        let (kind, payload) = input.encode();
        let bytes = frame(&Record {
            seq: self.next_seq,
            seconds: at.seconds,
            nanos: at.nanos,
            symbol: symbol.into(),
            kind,
            payload,
        })?;
        let next_seq = self
            .next_seq
            .checked_add(1)
            .ok_or_else(|| invalid("event sequence exhausted"))?;
        if let Err(error) = self
            .file
            .write_all(&bytes)
            .and_then(|()| self.file.sync_data())
        {
            self.failed = true;
            return Err(error);
        }
        self.next_seq = next_seq;
        Ok(())
    }

    fn sync(&mut self) -> io::Result<()> {
        self.file.sync_all()
    }
}

#[derive(Clone)]
pub(crate) struct RecordedClock(Arc<Mutex<Time>>);
impl Clock for RecordedClock {
    fn now(&self) -> Time {
        *self.0.lock().expect("recorded clock lock")
    }
}
#[derive(Clone)]
pub(crate) struct ReplayIds(Arc<AtomicU64>);
impl ExecIds for ReplayIds {
    fn next_id(&mut self) -> String {
        format!("X{}", self.0.fetch_add(1, Ordering::Relaxed))
    }
}

pub(crate) struct Journal<R> {
    log: EventLog,
    digest: crate::reproducibility::BusinessDigest,
    pub(crate) inspection: Option<crate::reproducibility::trace::Collector>,
    recording: Option<crate::reproducibility::Recording>,
    #[cfg(test)]
    pub(crate) trace: Vec<Vec<u8>>,
    pub(crate) engines: HashMap<String, Engine<R, RecordedClock, ReplayIds>>,
    clock: RecordedClock,
    ids: ReplayIds,
    schedule: Option<SessionSchedule>,
    stp_default: StpMode,
    detached: R,
    symbol_config: crate::symbol_config::SymbolConfig,
}

impl<R: Clone> Journal<R> {
    #[cfg(test)]
    pub(crate) fn open(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
        detached: R,
    ) -> io::Result<Self> {
        Self::open_configured(path, schedule, stp_default, detached, Default::default())
    }
    pub(crate) fn open_configured(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
        detached: R,
        symbol_config: crate::symbol_config::SymbolConfig,
    ) -> io::Result<Self> {
        let (log, records) =
            EventLog::open_configured(path, schedule, stp_default, &symbol_config)?;
        let mut journal = Self {
            log,
            digest: Default::default(),
            inspection: None,
            recording: None,
            #[cfg(test)]
            trace: Vec::new(),
            engines: HashMap::new(),
            clock: RecordedClock(Arc::new(Mutex::new(Time {
                seconds: 0,
                nanos: 0,
            }))),
            ids: ReplayIds(Arc::new(AtomicU64::new(1))),
            schedule,
            stp_default,
            detached,
            symbol_config,
        };
        for record in records {
            let input = Input::decode(&record)?;
            journal.execute(
                &record.symbol,
                Time {
                    seconds: record.seconds,
                    nanos: record.nanos,
                },
                input,
                journal.detached.clone(),
            );
        }
        tracing::info!(
            inputs = journal.log.next_seq - 1,
            symbols = journal.engines.len(),
            "replayed core event log"
        );
        Ok(journal)
    }

    pub(crate) fn record(
        dir: &Path,
        schedule: Option<SessionSchedule>,
        stp: StpMode,
        detached: R,
        config: crate::symbol_config::SymbolConfig,
        meta: &str,
    ) -> io::Result<Self> {
        let recording =
            crate::reproducibility::Recording::create(dir, schedule, stp, &config, meta)?;
        let mut journal =
            Self::open_configured(&recording.log_path(), schedule, stp, detached, config)?;
        journal.inspection = Some(Default::default());
        journal.recording = Some(recording);
        Ok(journal)
    }

    pub(crate) fn canonical_state(&self) -> Vec<u8> {
        let mut w = crate::reproducibility::StateWriter::default();
        w.text(crate::reproducibility::STATE_VERSION);
        w.u64(self.ids.0.load(Ordering::Relaxed));
        w.u64(self.engines.len() as u64);
        for symbol in self.symbols() {
            w.text(&symbol);
            w.bytes(&self.engines[&symbol].canonical_state());
        }
        w.0
    }

    pub(crate) fn summary(&self) -> crate::reproducibility::RunSummary {
        self.digest.summary(&self.canonical_state())
    }

    /// Called only after all symbol tasks drained successfully.
    pub(crate) fn finish(&mut self) -> io::Result<()> {
        self.sync()?;
        let summary = self.summary();
        if let Some(recording) = &mut self.recording {
            recording.finish(&summary)?;
        }
        Ok(())
    }

    /// Offline verification never opens the log for writing and never repairs it.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn replay_inspected(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
        detached: R,
        symbol_config: crate::symbol_config::SymbolConfig,
        complete: bool,
        inspect: bool,
    ) -> io::Result<Self> {
        Self::replay_observed(
            path,
            schedule,
            stp_default,
            detached,
            symbol_config,
            complete,
            inspect,
            |_, _, _, _| Ok(()),
        )
    }

    /// Read-only replay hook, used only by offline report generation.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn replay_observed(
        path: &Path,
        schedule: Option<SessionSchedule>,
        stp_default: StpMode,
        detached: R,
        symbol_config: crate::symbol_config::SymbolConfig,
        complete: bool,
        inspect: bool,
        mut observe: impl FnMut(
            u64,
            &str,
            &Engine<R, RecordedClock, ReplayIds>,
            &[Output<R>],
        ) -> io::Result<()>,
    ) -> io::Result<Self> {
        let mut file = File::open(path)?;
        let mut magic = [0; 8];
        file.read_exact(&mut magic)?;
        if &magic != MAGIC {
            return Err(invalid("invalid event log magic/version"));
        }
        let header = read_frame(&mut file)?.ok_or_else(|| invalid("missing event-log header"))?;
        let stp = if stp_default == StpMode::None {
            String::new()
        } else {
            format!(";stp_default={}", stp_default.as_str())
        };
        let text = symbol_config.canonical();
        let symbols = if text.is_empty() {
            String::new()
        } else {
            format!(";symbols={text}")
        };
        let config = format!("{REPLAY_VERSION};schedule={schedule:?}{stp}{symbols}");
        if header.seq != 0
            || header.kind != 0
            || !header.symbol.is_empty()
            || header.seconds != 0
            || header.nanos != 0
            || header.payload != config.as_bytes()
        {
            return Err(invalid("event log configuration/replay version mismatch"));
        }
        let len = file.metadata()?.len();
        let mut journal = Self {
            log: EventLog {
                file: file.try_clone()?,
                next_seq: 1,
                failed: false,
            },
            digest: Default::default(),
            inspection: None,
            recording: None,
            #[cfg(test)]
            trace: Vec::new(),
            engines: HashMap::new(),
            clock: RecordedClock(Arc::new(Mutex::new(Time {
                seconds: 0,
                nanos: 0,
            }))),
            ids: ReplayIds(Arc::new(AtomicU64::new(1))),
            schedule,
            stp_default,
            detached,
            symbol_config,
        };
        if inspect {
            journal.inspection = Some(Default::default());
        }
        while file.stream_position()? < len {
            let Some(record) = read_frame(&mut file)? else {
                if complete {
                    return Err(invalid("corrupt completed event log"));
                }
                break;
            };
            if record.seq != journal.log.next_seq {
                return Err(invalid("event log sequence gap"));
            }
            let input = Input::decode(&record)?;
            let outputs = journal.execute(
                &record.symbol,
                Time {
                    seconds: record.seconds,
                    nanos: record.nanos,
                },
                input,
                journal.detached.clone(),
            );
            observe(
                record.seq,
                &record.symbol,
                &journal.engines[&record.symbol],
                &outputs,
            )?;
            journal.log.next_seq = journal
                .log
                .next_seq
                .checked_add(1)
                .ok_or_else(|| invalid("event sequence exhausted"))?;
        }
        Ok(journal)
    }

    pub(crate) fn symbols(&self) -> Vec<String> {
        let mut symbols: Vec<_> = self.engines.keys().cloned().collect();
        symbols.sort();
        symbols
    }

    pub(crate) fn sync(&mut self) -> io::Result<()> {
        self.log.sync()
    }

    pub(crate) fn apply(
        &mut self,
        symbol: &str,
        at: Time,
        input: Input,
        owner: R,
    ) -> io::Result<Vec<Output<R>>> {
        if self.recording.as_ref().is_some_and(|r| r.is_finished()) {
            return Err(io::Error::other("recorded run is finalized"));
        }
        self.log.append(symbol, at, &input)?;
        let outputs = self.execute(symbol, at, input, owner);
        if let (Some(recording), Some(inspection)) = (&mut self.recording, &mut self.inspection) {
            recording.append_trace(&inspection.rows)?;
            inspection.rows.clear();
        }
        Ok(outputs)
    }

    pub(crate) fn tick(&mut self, symbol: &str, at: Time) -> io::Result<Vec<Output<R>>> {
        self.control(symbol, at, Input::Tick)
    }

    /// Applies an input that has no reporting owner.
    pub(crate) fn control(
        &mut self,
        symbol: &str,
        at: Time,
        input: Input,
    ) -> io::Result<Vec<Output<R>>> {
        self.apply(symbol, at, input, self.detached.clone())
    }

    fn execute(&mut self, symbol: &str, at: Time, input: Input, owner: R) -> Vec<Output<R>> {
        let captured = self.inspection.as_ref().map(|_| input.clone());
        *self.clock.0.lock().expect("recorded clock lock") = at;
        let inspect = self.inspection.is_some();
        let engine = self.engines.entry(symbol.to_string()).or_insert_with(|| {
            let engine = Engine::new(symbol, self.clock.clone(), self.ids.clone(), self.schedule)
                .with_stp_default(self.stp_default)
                .with_symbol_rules(self.symbol_config.get(symbol));
            if inspect {
                engine.with_diagnostics()
            } else {
                engine
            }
        });
        let outputs = match input {
            Input::New(order) => engine.new_order(order, owner),
            Input::Cancel(m) => engine.cancel(CancelMsg {
                order_id: m.order_id,
                sender: m.sender,
                client_cancel_id: m.client_cancel_id,
                output_tx: owner,
            }),
            Input::Replace(m) => engine.cancel_replace(CancelReplaceMsg {
                order_id: m.order_id,
                sender: m.sender,
                new_order_id: m.new_order_id,
                client_replace_id: m.client_replace_id,
                new_price: m.new_price,
                new_quantity: m.new_quantity,
                new_time_in_force: m.new_time_in_force,
                new_stop_price: m.new_stop_price,
                new_display_quantity: m.new_display_quantity,
                output_tx: owner,
            }),
            Input::Tick => engine.tick(),
            Input::Subscribe => engine.subscribe(owner),
            Input::MassCancel(m) => engine.mass_cancel(MassCancelMsg {
                sender: m.sender,
                side: m.side,
                client_request_id: m.client_request_id,
                reason: m.reason,
            }),
            Input::SessionControl(m) => engine.begin_opening_call(m.opening_call_seconds),
        };
        let diagnostics = engine.take_diagnostics();
        if let (Some(inspection), Some(input)) = (&mut self.inspection, captured) {
            // Appends advance next_seq before execute; read-only replay advances afterward.
            let seq = if self.recording.is_some() {
                self.log.next_seq - 1
            } else {
                self.log.next_seq
            };
            inspection.capture(seq, symbol, at, &input, &outputs, diagnostics);
        }
        self.digest.observe(symbol, &outputs);
        #[cfg(test)]
        for output in &outputs {
            let (tag, bytes) = match output {
                Output::ExecutionReport(_, m) => (1, m.encode_to_vec()),
                Output::Trade(m) => (2, m.encode_to_vec()),
                Output::TradingSessionStatus(m) => (3, m.encode_to_vec()),
                Output::AuctionInfo(m) => (4, m.encode_to_vec()),
                Output::MarketData(m) => (5, m.encode_to_vec()),
                Output::Snapshot(_, m) => (6, m.encode_to_vec()),
                Output::MassCancelCount(n) => (7, n.to_be_bytes().to_vec()),
            };
            let mut frame = vec![tag];
            frame.extend(bytes);
            self.trace.push(frame);
        }
        outputs
    }
}

#[cfg(test)]
pub(crate) mod tests;
