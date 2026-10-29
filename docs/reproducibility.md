# Recorded runs and reproducibility

Replaying the recorded commands reproduces the same ordered engine business
events and the same final market state as the live recorded run. It does not
reproduce a bot's decisions. Use a bundle to preserve an observed failure rather
than expecting a second live simulation with the same seed to produce that failure.

The boundary is command admission to the core journal. All execution reports for
all owners, public trade prints, trading session changes and auction indications
are observed before connection routing or delivery. Replaying preserves the global
journal command order and each command's output order. The final state includes
the shared next ExecID. The live recording uses that same journal-owned counter,
not the unrecorded server's process-global counter.

Not guaranteed: bot clocks/randomness/decisions; concurrent participant scheduling
before journal admission; connection identity; transport headers or timestamps;
TCP delivery order across sockets, backpressure or dropped market data; gateway
and FIX session state; admission/authentication rejects generated outside the
engine; aggregate transport mass-cancel responses. Book incrementals, snapshots,
status-query responses and mass-cancel counts are not business-digest inputs.
Individual engine cancellation reports are included. Engine-generated business
timestamps use the recorded injected clock and are included.

## Recording

```sh
FXP_RUN_RECORD=./runs/run-001 \
FXP_RUN_META_JSON='{"seed":7,"scenario":"stop-cascade"}' \
./fxp_core/target/release/fxp_core
```

Create the parent directory first. `FXP_RUN_RECORD` must name a new directory;
existing directories are refused, including incomplete bundles. It is mutually
exclusive with `FXP_EVENT_LOG`. Recording automatically enables the existing
synchronous journal and global command serialization. `FXP_RUN_META_JSON` is an
optional JSON object (default `{}`), for caller metadata such as simulator seeds.
Invalid JSON, duplicate keys and non-object metadata are rejected before the
server accepts traffic. Metadata is limited to 4 MiB and 64 nesting levels.

Session schedule, symbol rules and STP are still configured with
`FXP_SESSION_SCHEDULE`, `FXP_SYMBOL_CONFIG` and `FXP_STP_DEFAULT`. The effective
symbol configuration is copied as normalized CSV, including the empty file when
all symbols use built-in defaults. No config file is needed to replay the bundle.

Stop the core with SIGTERM or SIGINT and allow its normal shutdown drain to
complete. Only successful task draining and journal sync finalize a bundle.
The recorder does not finalize from `Drop`. A timeout, task failure, SIGKILL or
crash leaves the manifest incomplete. A finalized journal refuses further inputs.

Recording runs at **event-log speed**: every command still writes and calls
`sync_data` before execution; shutdown calls `sync_all`. No group commit, altered
acknowledgment policy or new event-log framing is introduced. Canonical event
hashing adds CPU work; no throughput improvement is claimed. Private bundle
files contain order/account information; Unix run directories are mode 0700 and
commands/config/manifest/expected files mode 0600 (subject to umask).

## Bundle format: fxp-run-v1

| File | Contents |
| --- | --- |
| `manifest.json` | `bundle_format`, `fxp_version`, build `git_sha`, `proto_schema_sha256`, `replay_version`, `canonical_version`, `state_version`, `symbol_config_file`, `symbol_config_sha256`, `session_schedule`, `stp_default`, caller `meta`, `start_time`, `end_time`, `complete`, `commands_sha256` |
| `symbols.csv` | Canonical copy of effective symbol rules, symbol-sorted; SHA-256 covers the exact UTF-8 bytes including trailing newlines |
| `commands.log` | Existing `FXPEVL01` / `engine-input-v1` event log, unchanged; recorded command clocks, global sequence and configuration header |
| `expected.json` | Canonical/state versions, global `{count,sha256}`, `symbols` map of `{count,sha256}`, and `final_state_sha256` |

Times in the manifest are Unix seconds as decimal strings with nine fractional
digits; they are provenance metadata, not replay inputs. `end_time` and
`commands_sha256` start as null, `complete` as false. Build provenance uses
`git rev-parse HEAD`; source archives can supply `FXP_BUILD_GIT_SHA` at build time
(`unknown` if neither source is available). A git SHA identifies the base commit;
use a clean committed source tree for attributable builds. The protocol schema
hash covers the exact checked-in `fxp_core/proto/fxp.proto` source bytes.

Finalization syncs `commands.log`, hashes its complete bytes, atomically writes
`expected.json`, then atomically publishes the completed manifest last. Files
are synced before rename and the directory is synced afterward. `.recording.lock`
and dot-prefixed temporary files are internal bookkeeping. An interrupted
finalization can leave an orphan expected file; an incomplete manifest makes all
expected digests non-authoritative. Incomplete replays never claim verification.

## Offline API and verification

`fxp_core::reproducibility::replay_bundle(path)` returns `ReplayResult` with
`verified`, `RunSummary` and a canonical final-state byte dump. There is no CLI in
this PR. The API opens the log read-only, checks versions/configuration and
replays commands without appending catch-up ticks or truncating torn tails.

A complete bundle must match the log's SHA-256 before decoding. All event counts,
per-symbol/global digests and the final-state hash must then match `expected.json`.
Missing expected data, an altered command log/config, unknown bundle/replay/event/
state versions, or a schema mismatch produce a clear error. The reader also
checks the event-log configuration header and gap-free global command sequence.
An incomplete bundle replays the valid committed prefix, tolerating a torn final
frame, and returns `verified=false`; it cannot attest to command-log integrity.
These hashes detect accidental changes, not malicious replacement of both the
log and its unsigned manifest/expected data.

## Canonical business bytes: fxp-business-v1

Each stream hashes the UTF-8 domain prefix `fxp-business-v1` followed by one NUL,
then concatenated event frames. An empty stream hashes the prefix alone.
Each frame is `tag:u8 || payload_length:u64 big-endian || payload`:

| Tag | Protobuf payload |
| --- | --- |
| 1 | `ExecutionReport`, `header` removed |
| 2 | `TradeCaptureReport`, `header` removed |
| 3 | `TradingSessionStatus` |
| 4 | `AuctionInfo` |

Protobuf fields are encoded in ascending field-number order with standard proto3
scalar/default omission and UTF-8 string encoding. These message schemas have no
maps or floats. Optional message presence (including timestamps) is preserved.
All business fields, IDs and engine timestamps are included. Routing handles and
headers are excluded. This version is pinned to the schema hash; changing field
semantics/encoding requires a canonical version bump.

The global stream consumes outputs while holding the journal's global execution
lock, in increasing command sequence, then output position. Each symbol stream
consumes its own subsequence in the same order. Command sequence numbers are not
hashed as extra event fields; commands producing no business output do not change
the business digest. Symbol entries exist even if their event count is zero.
`expected.json` maps symbol names in lexical order. Counts count events, not
commands or deliveries; the two sides' execution reports are separate events.

`scripts/repro_vectors.py` independently encodes all four event kinds and computes
SHA-256 with Python's standard library (no Rust/protobuf dependency). The Rust
vector test compares exact frame bytes, frame hashes and the domain-separated
stream hash. This prevents an encoder and its digest test sharing one bug.

## Canonical state bytes: fxp-state-v1

`StateWriter` uses explicit binary fields, never Rust Debug output, pointer
addresses or randomized map iteration. Integers are fixed-width big-endian;
counts, booleans, enum codes and lengths are u64, signed prices/times are i64,
and notional/auction imbalance is i128. Strings and blobs are u64 byte length
followed by bytes. Options have a u64 presence flag followed by their value.
Maps sort by their UTF-8 keys, levels sort numerically and FIFO queues retain
priority order. State hashes are SHA-256 of these entire dump bytes.

The global dump contains the length-prefixed state version, next shared ExecID,
symbol count, then symbol-sorted `(symbol, engine_dump_blob)` entries. Each engine
dump, in order, contains:

1. State version and symbol; buy and sell levels with orders in FIFO order;
   lookup orders; cumulative fills; displayed slices; arrival map; next book
   arrival and last-added ID.
2. Parked stops in arrival order; auction market orders and held closing orders.
3. Live reporting state by ID: owner/trader, TIF, expiry, arrival, side, total/
   display/cumulative quantities, notional, account and client ID. Connection
   handles are excluded.
4. Session phase, optional last price, symbol trade sequence, next engine arrival,
   last-seen clock, optional operator call deadline and indicative auction result.
5. Schedule, STP default, symbol rules, and an empty peg section. This engine has
   no peg order type; adding one requires encoding its state and a version bump.
6. Known IDs and reports in the engine's retained status-history map (including
   its bounded terminal history), and terminal-history queue in eviction order.

Order encoding is ID, trader, symbol, price, total leaves, display cap, side,
order type, TIF, millisecond timestamp, stop price, expiry, account, client ID,
STP mode name and group. Side codes are buy=1/sell=2; order types are market=1,
limit=2, stop=3, stop-limit=4; TIF codes are day=1, GTC=2, IOC=3, FOK=4,
at-open=5, at-close=6, GTD=7. Reporting side/phase use protobuf enum values.
Iceberg reserve is captured by total leaves, display cap and current displayed
slice; replenishment priority is captured by queue order and arrival maps.

The detailed audit is in [determinism-audit.md](determinism-audit.md).

## Tests and scope

`cargo test --locked` includes 200 seeded multi-symbol command histories comparing
complete ordered Engine outputs (including non-business outputs), canonical state,
digests and next ExecID after replay. Histories cover new/cancel/replace/mass
cancel/ticks/subscriptions, all order types/TIFs, icebergs, STP and session auctions.
It also replays one bundle in 20 independent processes, exercising randomized
HashMap seeds, and checks tampering, versions and incomplete/torn bundles.

`bash scripts/repro_bundle_smoke.sh` runs the existing simulator 20 times through
real core/gateway sockets with four concurrent participants and two symbols.
Every live run produces a separate bundle and must independently verify offline.
It is part of the existing `e2e` CI job, already required by `ci-success`.
These are correctness checks, not performance measurements.

Deliberately deferred: user CLI, controlling bots' clocks/randomness, UI, UDP
market data, io_uring, pinned thread-per-core, fixed-layout messages, group commit
and journal-backed sessions. No changes to wire schemas or bot logic are made.
