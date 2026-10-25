// SPDX-License-Identifier: MIT

use std::{
    fs::File,
    io::{BufWriter, Write},
    sync::{Mutex, OnceLock},
};
static SINK: OnceLock<Option<Mutex<BufWriter<File>>>> = OnceLock::new();

#[cfg(target_os = "linux")]
fn monotonic_ns() -> i64 {
    #[repr(C)]
    struct Timespec {
        sec: std::ffi::c_long,
        nsec: std::ffi::c_long,
    }
    unsafe extern "C" {
        fn clock_gettime(clock: std::ffi::c_int, ts: *mut Timespec) -> std::ffi::c_int;
    }
    let mut ts = Timespec { sec: 0, nsec: 0 };
    // SAFETY: valid writable repr(C) timespec; Linux CLOCK_MONOTONIC is 1.
    if unsafe { clock_gettime(1, &mut ts) } != 0 {
        return 0;
    }
    ts.sec * 1_000_000_000 + ts.nsec
}
#[cfg(not(target_os = "linux"))]
fn monotonic_ns() -> i64 {
    0
}

pub fn init() -> std::io::Result<()> {
    let sink = match std::env::var_os("FXP_HOP_TRACE") {
        None => None,
        Some(path) if path.is_empty() => None,
        Some(path) => {
            if monotonic_ns() == 0 {
                return Err(std::io::Error::other("CLOCK_MONOTONIC unavailable"));
            }
            let mut options = std::fs::OpenOptions::new();
            options.create(true).truncate(true).write(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.mode(0o600);
            }
            Some(Mutex::new(BufWriter::with_capacity(
                256 * 1024,
                options.open(path)?,
            )))
        }
    };
    let _ = SINK.set(sink);
    Ok(())
}
pub fn enabled() -> bool {
    matches!(SINK.get(), Some(Some(_)))
}
pub fn event(stage: &str, id: &str) {
    let Some(Some(sink)) = SINK.get() else {
        return;
    };
    let ns = monotonic_ns();
    // TSV uses escaped debug strings for arbitrary order IDs, not raw input.
    if let Ok(mut writer) = sink.lock() {
        let _ = writeln!(writer, "{stage}\t{id:?}\t{ns}");
    }
}
pub fn frame(stage: &str, raw: &[u8]) {
    if !enabled() {
        return;
    }
    use prost::Message;
    if let Ok(msg) = crate::fxp::FxpMessage::decode(raw)
        && let Some(crate::fxp::fxp_message::Payload::ExecutionReport(r)) = msg.payload
        && r.execution_type == crate::fxp::ExecutionType::ExecNew as i32
    {
        event(stage, &r.order_id);
    }
}
pub fn flush() {
    if let Some(Some(sink)) = SINK.get()
        && let Ok(mut writer) = sink.lock()
    {
        let _ = writer.flush();
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    #[test]
    fn monotonic_clock_never_goes_backwards() {
        let first = super::monotonic_ns();
        assert!(first > 0);
        assert!(super::monotonic_ns() >= first);
    }
}
