# Replay memory measurements

Measured on Linux with separate release CLI processes and `wait4.ru_maxrss` (KiB). Fixtures are deterministic engine recordings with alternating, traded one-unit orders, no growing resting book. Event counts are business events; trace rows also include received commands. This is one workload, not a universal upper bound.

The old verifier retained both the complete recorded and replayed JSON traces. Verification now compares each command’s rows against a buffered trace reader, discards successful rows, and keeps the first differing command for verification diagnostics. Reports retain sparse differing rows so forensic timelines can be reconstructed without replaying the engine or charging the event budget again. Reports move the verified trace into the display timeline after deriving whole-run metadata and privacy mappings. They trim canonical/protobuf encodings in place, avoid a second complete trace, and release replay state before serialization. JSON encoding appends recursively to one buffer rather than allocating and joining encoded copies of every row. Full-window report row limits are checked during inspection before allocating the report document.

Large HTML payloads use versioned zlib/base64 JSON with an offline, bounded `DecompressionStream` decoder. The report schema, anonymization, CSP hashes, and decoded data limit remain enforced. The private worker’s 4 MiB artifact cap need not increase. JSON exports remain unchanged.

| Business events | Command | RSS MiB / exit (768 MiB flag) | Final HTML bytes |
|---:|---|---:|---:|
| 1000 | verify | 10.4 / 0 | 0 |
| 1000 | report | 12.8 / 0 | 60705 |
| 10000 | verify | 10.4 / 0 | 0 |
| 10000 | report | 104.6 / 0 | 459746 |
| 25000 | verify | 14.7 / 0 | 0 |
| 25000 | report | 255.9 / 0 | 1115723 |
| 45000 | verify | 26.0 / 0 | 0 |
| 45000 | report | 454.1 / 0 | 1964687 |
| 100000 | verify | 52.8 / 0 | 0 |
| 100000 | report | 510.0 / 2 | 0 |

The 45k-event regression exceeds the roughly 43k business events observed in a
300-second sandbox quiet-book run. Before the row-move/single-buffer fix, this
fixture reproducibly exits 3 at the 768 MiB watchdog with no report; afterwards
it succeeds at 454.1 MiB and produces 1,964,687 bytes of HTML. This traded
synthetic workload is a count-based regression.
A fresh native 300-second quiet-book run (seed 42, two symbols, two participants,
no bot) recorded 41,193 business events. On that same bundle the old
report CLI exits 3 at the 768 MiB watchdog. The fixed CLI verifies successfully,
generates the report at 545.6 MiB in 5.61 seconds, and produces
2,662,935 bytes of HTML; a second generation succeeds identically.
The recording used native binaries with FXP provenance
`21ede7664e385c441370122097999c4a87b42dc4`, rather than the sandbox's older
published replay image. This checks the real default workload, not the complete
Compose worker publication path.


The 100k-event full report exceeds the existing 100,000 timeline-row limit and exits 2 with that explicit diagnostic. It is rejected while reading the trace; it does not exhaust 768 MiB. A narrower report window is supported subject to the same bounded process limit. The 100k fixture also exceeds the sandbox’s separate 64 MiB per-file archive limit.

The sandbox retains 768 MiB, with headroom above the largest observed supported report / early row-limit rejection (~510 MiB). Verification itself no longer needs that budget for these fixtures. Keep the process watchdog and container cgroup limits: event size, distinct owners/orders, book frames and hostile JSON can change the curve. A container reserves another 256 MiB for extraction tmpfs, wrapper and overhead, with another 160 MiB for two-input diff. Admission must reserve the worst supported command against a finite total budget.

Reproduce (from `fxp_core/`):

```sh
FXP_PROFILE_DIR=/tmp/replay-profile cargo test --release --lib generate_replay_profile_bundles -- --ignored --nocapture
cargo build --release --bin fxp-replay
python3 ../scripts/profile-replay.py target/release/fxp-replay /tmp/replay-profile --memory-mib 768 --check
```

Every measured command uses `--max-events 1000000 --max-bytes 134217728 --max-wall-seconds 25 --max-memory-mb 768`, matching the sandbox verify/report flags. Explicit `max-bytes`, memory, wall and event checks remain active. Unchanged golden report JSON and divergence tests protect semantics.

Three original stored normal-load reports independently reproduced the sandbox
output-size failure: offline verify/report both succeed at 256 MiB, but HTML is
6,816,545 / 8,213,845 / 8,014,312 bytes. The sandbox's original isolated wrapper
rejects each at its 4 MiB artifact cap with exit 2. The new HTML is 416,894 /
499,942 / 484,702 bytes, respectively, and the same bundles verify and publish
reports successfully through the worker. The output cap remains unchanged.

The image release smoke decodes the versioned HTML payload and compares the whole
document with the separate JSON export before checking image provenance. It does
not search compressed bytes for plaintext image references. Duplicate fields,
trailing streams, nested encodings and decoded size mismatches fail validation.
