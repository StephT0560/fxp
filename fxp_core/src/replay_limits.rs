// SPDX-License-Identifier: MIT
//! Optional budgets for offline CLI work; no effect on live matching or recording.
use std::cell::RefCell;
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File};
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
static PROCESS: AtomicBool = AtomicBool::new(false);
static REPORTED: std::sync::Mutex<bool> = std::sync::Mutex::new(false);
pub(crate) fn process_mode() {
    PROCESS.store(true, Ordering::Relaxed);
}
pub(crate) fn report(err: &mut impl Write, name: &str) {
    if PROCESS.load(Ordering::Relaxed) {
        let mut reported = REPORTED.lock().unwrap_or_else(|e| e.into_inner());
        if !*reported {
            let _ = writeln!(err, "{}", error_line(name));
            let _ = err.flush();
            *reported = true;
        }
    } else {
        let _ = writeln!(err, "{}", error_line(name));
    }
}

pub(crate) const EXIT_LIMIT: i32 = 3;
#[derive(Debug)]
pub(crate) struct Exceeded(pub &'static str);
impl std::fmt::Display for Exceeded {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for Exceeded {}
fn breach(name: &'static str) -> io::Error {
    io::Error::other(Exceeded(name))
}
pub(crate) fn error_line(name: &str) -> String {
    format!("{{\"error\":\"resource_limit\",\"limit\":\"{name}\"}}")
}
#[derive(Clone, Default)]
pub(crate) struct Limits {
    events: Option<u64>,
    bytes: Option<u64>,
    wall: Option<Duration>,
    memory: Option<u64>,
}
impl Limits {
    pub(crate) fn extract(args: Vec<String>) -> io::Result<(Self, Vec<String>)> {
        let mut limits = Self::default();
        let mut rest = Vec::new();
        let mut seen = BTreeSet::new();
        let mut args = args.into_iter();
        while let Some(arg) = args.next() {
            let (name, inline) = arg
                .split_once('=')
                .map_or((arg.as_str(), None), |(k, v)| (k, Some(v)));
            if ![
                "--max-events",
                "--max-bytes",
                "--max-wall-seconds",
                "--max-memory-mb",
            ]
            .contains(&name)
            {
                rest.push(arg);
                continue;
            }
            if !seen.insert(name.to_string()) {
                return Err(io::Error::other("duplicate resource limit"));
            }
            let value = inline
                .map(str::to_string)
                .or_else(|| args.next())
                .ok_or_else(|| io::Error::other("missing resource limit value"))?;
            let integer = || {
                value
                    .parse::<u64>()
                    .map_err(|_| io::Error::other("resource limit requires an unsigned integer"))
            };
            match name {
                "--max-events" => limits.events = Some(integer()?),
                "--max-bytes" => limits.bytes = Some(integer()?),
                "--max-memory-mb" => {
                    let n = integer()?
                        .checked_mul(1024 * 1024)
                        .filter(|n| *n > 0)
                        .ok_or_else(|| io::Error::other("invalid memory limit"))?;
                    // Never silently promise enforcement on unsupported hosts.
                    resident_bytes()?;
                    limits.memory = Some(n);
                }
                "--max-wall-seconds" => {
                    let n = value
                        .parse::<f64>()
                        .map_err(|_| io::Error::other("invalid wall limit"))?;
                    let duration = Duration::try_from_secs_f64(n)
                        .ok()
                        .filter(|d| !d.is_zero())
                        .ok_or_else(|| {
                            io::Error::other("wall limit must be finite and positive")
                        })?;
                    limits.wall = Some(duration);
                }
                _ => unreachable!(),
            }
        }
        Ok((limits, rest))
    }
    fn check(&self, start: Instant) -> io::Result<()> {
        if self.wall.is_some_and(|d| start.elapsed() >= d) {
            return Err(breach("max-wall-seconds"));
        }
        if let Some(max) = self.memory
            && resident_bytes()? > max
        {
            return Err(breach("max-memory-mb"));
        }
        Ok(())
    }
    pub(crate) fn active(&self) -> bool {
        self.events.is_some()
            || self.bytes.is_some()
            || self.wall.is_some()
            || self.memory.is_some()
    }
    pub(crate) fn enter(&self) -> Scope {
        let previous = CURRENT.with(|slot| {
            slot.replace(self.active().then(|| Budget {
                limits: self.clone(),
                start: Instant::now(),
                events: 0,
                bytes: 0,
                files: BTreeMap::new(),
            }))
        });
        Scope(previous)
    }
    // A process-only watchdog also covers slow parsers/report rendering and blocked I/O.
    pub(crate) fn watch(&self) -> Watch {
        let (stop, rx) = std::sync::mpsc::channel();
        let limits = self.clone();
        let thread = if self.wall.is_some() || self.memory.is_some() {
            Some(std::thread::spawn(move || {
                let start = Instant::now();
                loop {
                    if let Err(error) = limits.check(start) {
                        if let Some(limit) =
                            error.get_ref().and_then(|e| e.downcast_ref::<Exceeded>())
                        {
                            report(&mut std::io::stderr(), limit.0);
                            std::process::exit(EXIT_LIMIT);
                        }
                        eprintln!("invalid: resource monitor unavailable");
                        std::process::exit(2);
                    }
                    if rx.recv_timeout(Duration::from_millis(10))
                        != Err(std::sync::mpsc::RecvTimeoutError::Timeout)
                    {
                        break;
                    }
                }
            }))
        } else {
            None
        };
        Watch(stop, thread)
    }
}
pub(crate) struct Watch(
    std::sync::mpsc::Sender<()>,
    Option<std::thread::JoinHandle<()>>,
);
impl Drop for Watch {
    fn drop(&mut self) {
        let _ = self.0.send(());
        if let Some(t) = self.1.take() {
            let _ = t.join();
        }
    }
}
struct Budget {
    limits: Limits,
    start: Instant,
    events: u64,
    bytes: u64,
    files: BTreeMap<PathBuf, u64>,
}
thread_local! { static CURRENT: RefCell<Option<Budget>> = const { RefCell::new(None) }; }
pub(crate) struct Scope(Option<Budget>);
impl Drop for Scope {
    fn drop(&mut self) {
        CURRENT.with(|s| {
            s.replace(self.0.take());
        });
    }
}
pub(crate) fn checkpoint() -> io::Result<()> {
    CURRENT.with(|slot| {
        let state = slot.borrow();
        if let Some(b) = state.as_ref() {
            b.limits.check(b.start)?;
        }
        Ok(())
    })
}
/// Work events are journal records, trace rows parsed, and replay outputs, summed
/// across both diff inputs and repeated report passes. Not only business prints.
pub(crate) fn events(n: usize) -> io::Result<()> {
    checkpoint()?;
    CURRENT.with(|slot| {
        if let Some(b) = slot.borrow_mut().as_mut() {
            b.events = b
                .events
                .checked_add(n as u64)
                .ok_or_else(|| breach("max-events"))?;
            if b.limits.events.is_some_and(|limit| b.events > limit) {
                return Err(breach("max-events"));
            }
        }
        Ok(())
    })
}
/// Fixed bundle filenames only. Reject links and special files, including FIFOs.
/// Worker inputs must remain immutable/read-only for the entire invocation.
pub(crate) fn input(path: &Path) -> io::Result<File> {
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(io::Error::other(
            "bundle input must be a regular file, not a link",
        ));
    }
    checkpoint()?;
    let canonical = path.canonicalize()?;
    CURRENT.with(|slot| {
        if let Some(b) = slot.borrow_mut().as_mut() {
            let previous = b.files.get(&canonical).copied().unwrap_or(0);
            if metadata.len() > previous {
                b.files.insert(canonical, metadata.len());
                b.bytes = b
                    .bytes
                    .checked_add(metadata.len() - previous)
                    .ok_or_else(|| breach("max-bytes"))?;
                if b.limits.bytes.is_some_and(|limit| b.bytes > limit) {
                    return Err(breach("max-bytes"));
                }
            }
        }
        Ok::<_, io::Error>(())
    })?;
    File::open(path)
}
pub(crate) fn preflight(dir: &Path) -> io::Result<()> {
    for name in [
        "manifest.json",
        "symbols.csv",
        "commands.log",
        "expected.json",
        "events.jsonl",
    ] {
        let path = dir.join(name);
        match fs::symlink_metadata(&path) {
            Ok(_) => {
                input(&path)?;
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => (),
            Err(e) => return Err(e),
        }
    }
    Ok(())
}
pub(crate) fn read_text(path: &Path) -> io::Result<String> {
    let mut file = input(path)?;
    // Enforce actual bytes as well as metadata; do not allocate the whole file
    // first. Immutable bundle mounts prevent replacement between metadata/open.
    let cap = CURRENT.with(|s| s.borrow().as_ref().and_then(|b| b.limits.bytes));
    let mut value = String::new();
    if let Some(max) = cap {
        file.take(max.saturating_add(1))
            .read_to_string(&mut value)?;
        if value.len() as u64 > max {
            return Err(breach("max-bytes"));
        }
    } else {
        file.read_to_string(&mut value)?;
    }
    checkpoint()?;
    Ok(value)
}
fn resident_bytes() -> io::Result<u64> {
    #[cfg(target_os = "linux")]
    {
        let text = fs::read_to_string("/proc/self/status")?;
        let kb = text
            .lines()
            .find_map(|line| {
                line.strip_prefix("VmRSS:")
                    .and_then(|v| v.split_whitespace().next())
                    .and_then(|v| v.parse::<u64>().ok())
            })
            .ok_or_else(|| io::Error::other("RSS unavailable"))?;
        kb.checked_mul(1024)
            .ok_or_else(|| io::Error::other("RSS overflow"))
    }
    #[cfg(not(target_os = "linux"))]
    Err(io::Error::new(
        io::ErrorKind::Unsupported,
        "--max-memory-mb requires Linux /proc",
    ))
}
