# Replay memory measurements

Measured on Linux with separate release CLI processes and `wait4.ru_maxrss` (KiB). Fixtures are deterministic engine recordings with alternating, traded one-unit orders, no growing resting book. Event counts are business events; trace rows also include received commands. This is one workload, not a universal upper bound.

The old verifier retained both the complete recorded and replayed JSON traces. Verification now compares each command’s rows against a buffered trace reader, discards successful rows, and replays only divergent cases to retain forensic timelines. Reports retain one historical trace, trim canonical/protobuf encodings before copying display rows, and release replay state before serialization. Full-window report row limits are checked during inspection before allocating the report document.

Large HTML payloads use versioned zlib/base64 JSON with an offline, bounded `DecompressionStream` decoder. The report schema, anonymization, CSP hashes, and decoded data limit remain enforced. The private worker’s 4 MiB artifact cap need not increase. JSON exports remain unchanged.

| Business events | Command | Before: RSS MiB / exit (256 MiB flag) | After: RSS MiB / exit (768 MiB flag) | Final HTML bytes |
|---:|---|---:|---:|---:|
| 1000 | verify | 16.3 / 0 | 10.2 / 0 | 0 |
| 1000 | report | 30.7 / 0 | 21.4 / 0 | 60697 |
| 10000 | verify | 143.8 / 0 | 10.2 / 0 | 0 |
| 10000 | report | 265.3 / 3 | 193.2 / 0 | 459750 |
| 25000 | verify | 255.7 / 3 | 14.9 / 0 | 0 |
| 25000 | report | 255.7 / 3 | 474.5 / 0 | 1115711 |
| 100000 | verify | 255.8 / 3 | 52.9 / 0 | 0 |
| 100000 | report | 255.8 / 3 | 510.1 / 2 | 0 |

The 100k-event full report exceeds the existing 100,000 timeline-row limit and exits 2 with that explicit diagnostic. It is rejected while reading the trace; it does not exhaust 768 MiB. A narrower report window is supported subject to the same bounded process limit. The 100k fixture also exceeds the sandbox’s separate 64 MiB per-file archive limit.

The sandbox selects 768 MiB: approximately 50% headroom above the largest observed supported report / early row-limit rejection (~510 MiB). Verification itself no longer needs that budget for these fixtures. Keep the process watchdog and container cgroup limits: event size, distinct owners/orders, book frames and hostile JSON can change the curve. A container reserves another 256 MiB for extraction tmpfs, wrapper and overhead, with another 160 MiB for two-input diff. Admission must reserve the worst supported command against a finite total budget.

Reproduce (from `fxp_core/`):

```sh
FXP_PROFILE_DIR=/tmp/replay-profile cargo test --release --lib generate_replay_profile_bundles -- --ignored --nocapture
cargo build --release --bin fxp-replay
python3 ../scripts/profile-replay.py target/release/fxp-replay /tmp/replay-profile --memory-mib 768
```

Every measured command uses `--max-events 1000000 --max-bytes 134217728 --max-wall-seconds 25 --max-memory-mb 768`, matching the sandbox verify/report flags. Explicit `max-bytes`, memory, wall and event checks remain active. Unchanged golden report JSON and divergence tests protect semantics.
