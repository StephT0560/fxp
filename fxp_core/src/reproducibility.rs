// SPDX-License-Identifier: MIT
//! Canonical business event digests and final-state encoding.
use crate::engine::Output;
use prost::Message;
use sha2::{Digest,Sha256};
use std::collections::BTreeMap;
pub const CANONICAL_VERSION: &str = "fxp-business-v1";
pub const STATE_VERSION: &str = "fxp-state-v1";
pub fn sha256(bytes: &[u8]) -> String { format!("{:x}", Sha256::digest(bytes)) }
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


#[cfg(test)]
mod vectors;
