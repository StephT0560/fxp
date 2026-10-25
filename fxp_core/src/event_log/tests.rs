// SPDX-License-Identifier: MIT
use super::*;
use crate::order_book::order::StpMode;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering as AtomicOrdering};

pub(crate) struct TestDir(pub(crate) PathBuf);
impl TestDir {
    pub(crate) fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("target/event-log-tests")
            .join(format!(
                "{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, AtomicOrdering::Relaxed)
            ));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }
    pub(crate) fn log(&self) -> PathBuf {
        self.0.join("events.log")
    }
}
impl Drop for TestDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}
fn time() -> Time {
    Time {
        seconds: 123,
        nanos: 456,
    }
}
fn order() -> fxp::NewOrderSingle {
    fxp::NewOrderSingle {
        symbol: "TEST".into(),
        sender: "owner".into(),
        order_id: "B".into(),
        price: 100,
        quantity: 5,
        side: fxp::OrderSide::Buy as i32,
        order_type: fxp::OrderType::Limit as i32,
        time_in_force: fxp::TimeInForce::Gtc as i32,
        ..Default::default()
    }
}

#[test]
fn record_round_trip_and_checksum_known_vector() {
    assert_eq!(crc32(b"123456789"), 0xcbf4_3926);
    let dir = TestDir::new();
    let (mut log, records) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    assert!(records.is_empty());
    log.append("TEST", time(), &Input::New(order())).unwrap();
    drop(log);
    let (_, records) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    assert_eq!(records.len(), 1);
    assert_eq!(
        (records[0].seq, records[0].seconds, records[0].nanos),
        (1, 123, 456)
    );
    let Input::New(restored) = Input::decode(&records[0]).unwrap() else {
        panic!("wrong kind");
    };
    assert_eq!(restored, order());
}

#[test]
fn every_torn_frame_offset_truncates_to_last_good_record() {
    let record = Record {
        seq: 2,
        seconds: 123,
        nanos: 456,
        symbol: "TEST".into(),
        kind: 4,
        payload: Vec::new(),
    };
    let bytes = frame(&record).unwrap();
    for cut in 1..bytes.len() {
        let dir = TestDir::new();
        let (mut log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
        log.append("TEST", time(), &Input::Tick).unwrap();
        let good = log.file.metadata().unwrap().len();
        drop(log);
        let mut file = OpenOptions::new().append(true).open(dir.log()).unwrap();
        file.write_all(&bytes[..cut]).unwrap();
        drop(file);
        let (mut log, records) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
        assert_eq!(records.len(), 1, "cut {cut}");
        assert_eq!(log.file.metadata().unwrap().len(), good);
        log.append("TEST", time(), &Input::Tick).unwrap();
        drop(log);
        assert_eq!(
            EventLog::open(&dir.log(), None, StpMode::None)
                .unwrap()
                .1
                .len(),
            2
        );
    }
}

#[test]
fn corrupt_checksum_stops_at_good_prefix_and_discards_entire_suffix() {
    let dir = TestDir::new();
    let (mut log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    log.append("TEST", time(), &Input::Tick).unwrap();
    let good = log.file.metadata().unwrap().len();
    log.append("TEST", time(), &Input::Tick).unwrap();
    log.append("TEST", time(), &Input::Tick).unwrap();
    drop(log);
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(dir.log())
        .unwrap();
    file.seek(SeekFrom::Start(good + 5)).unwrap();
    file.write_all(&[0xff]).unwrap();
    drop(file);
    let (log, records) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    assert_eq!(records.len(), 1);
    assert_eq!(log.file.metadata().unwrap().len(), good);
}

#[test]
fn invalid_lengths_are_bounded_and_repaired() {
    for length in [0_u32, MAX_RECORD as u32 + 1, u32::MAX] {
        let dir = TestDir::new();
        let (log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
        let good = log.file.metadata().unwrap().len();
        drop(log);
        OpenOptions::new()
            .append(true)
            .open(dir.log())
            .unwrap()
            .write_all(&length.to_be_bytes())
            .unwrap();
        let (log, records) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
        assert!(records.is_empty());
        assert_eq!(log.file.metadata().unwrap().len(), good);
    }
}

#[test]
fn concurrent_writer_and_changed_configuration_fail_without_truncation() {
    let dir = TestDir::new();
    let (log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    assert!(EventLog::open(&dir.log(), None, StpMode::None).is_err());
    let len = log.file.metadata().unwrap().len();
    drop(log);
    assert!(
        EventLog::open(
            &dir.log(),
            Some("close=16:00".parse().unwrap()),
            StpMode::None
        )
        .is_err()
    );
    assert!(EventLog::open(&dir.log(), None, StpMode::CancelBoth).is_err());
    assert_eq!(std::fs::metadata(dir.log()).unwrap().len(), len);
    assert!(EventLog::open(&dir.log(), None, StpMode::None).is_ok());
}

#[test]
fn write_failure_does_not_apply_input_and_poisoned_writer_cannot_continue() {
    let dir = TestDir::new();
    let mut journal = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    journal.log.file = File::open(dir.log()).unwrap();
    assert!(
        journal
            .apply("TEST", time(), Input::New(order()), 7)
            .is_err()
    );
    assert!(journal.engines.is_empty());
    assert!(journal.log.failed);
    assert!(journal.tick("TEST", time()).is_err());
    assert_eq!(journal.log.next_seq, 1);
}

#[test]
fn semantic_corruption_and_bad_header_fail_closed() {
    for kind in [1, 99] {
        let dir = TestDir::new();
        let (log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
        drop(log);
        let record = Record {
            seq: 1,
            seconds: 0,
            nanos: 0,
            symbol: "TEST".into(),
            kind,
            payload: vec![0xff],
        };
        OpenOptions::new()
            .append(true)
            .open(dir.log())
            .unwrap()
            .write_all(&frame(&record).unwrap())
            .unwrap();
        let len = std::fs::metadata(dir.log()).unwrap().len();
        assert!(Journal::open(&dir.log(), None, StpMode::None, 0).is_err());
        assert_eq!(std::fs::metadata(dir.log()).unwrap().len(), len);
    }
    let dir = TestDir::new();
    std::fs::write(dir.log(), b"not a log").unwrap();
    assert!(EventLog::open(&dir.log(), None, StpMode::None).is_err());
    assert_eq!(std::fs::read(dir.log()).unwrap(), b"not a log");
}

#[test]
fn sequence_gap_does_not_silently_delete_history() {
    let dir = TestDir::new();
    let (log, _) = EventLog::open(&dir.log(), None, StpMode::None).unwrap();
    drop(log);
    let record = Record {
        seq: 2,
        seconds: 0,
        nanos: 0,
        symbol: "TEST".into(),
        kind: 4,
        payload: Vec::new(),
    };
    OpenOptions::new()
        .append(true)
        .open(dir.log())
        .unwrap()
        .write_all(&frame(&record).unwrap())
        .unwrap();
    let len = std::fs::metadata(dir.log()).unwrap().len();
    assert!(EventLog::open(&dir.log(), None, StpMode::None).is_err());
    assert_eq!(std::fs::metadata(dir.log()).unwrap().len(), len);
}

#[test]
fn iceberg_replay_restores_partial_slice_and_replacement_presence() {
    fn snapshot(j: &mut Journal<u32>) -> fxp::MarketDataSnapshot {
        j.apply("TEST", time(), Input::Subscribe, 1)
            .unwrap()
            .into_iter()
            .find_map(|o| {
                if let Output::Snapshot(_, s) = o {
                    Some(s)
                } else {
                    None
                }
            })
            .unwrap()
    }
    let dir = TestDir::new();
    let mut j = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    let mut reserve = order();
    reserve.display_quantity = 2;
    reserve.quantity = 10;
    j.apply("TEST", time(), Input::New(reserve), 1).unwrap();
    let mut sell = order();
    sell.order_id = "S".into();
    sell.sender = "other".into();
    sell.side = fxp::OrderSide::Sell as i32;
    sell.quantity = 3;
    j.apply("TEST", time(), Input::New(sell), 2).unwrap();
    assert_eq!(snapshot(&mut j).bid_levels[0].size, 1);
    drop(j);
    let mut j = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    assert_eq!(snapshot(&mut j).bid_levels[0].size, 1);
    j.apply(
        "TEST",
        time(),
        Input::Replace(fxp::OrderCancelReplaceRequest {
            symbol: "TEST".into(),
            order_id: "B".into(),
            new_order_id: "R".into(),
            sender: "owner".into(),
            new_display_quantity: Some(0),
            ..Default::default()
        }),
        1,
    )
    .unwrap();
    assert_eq!(snapshot(&mut j).bid_levels[0].size, 7);
    drop(j);
    let mut j = Journal::open(&dir.log(), None, StpMode::None, 0).unwrap();
    assert_eq!(snapshot(&mut j).bid_levels[0].size, 7);
}
