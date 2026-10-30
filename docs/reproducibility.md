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
| `events.jsonl` (new recordings) | Command receipts, canonical business frames, and opt-in engine causal diagnostics; version/hash in `trace_version` / `trace_sha256` |

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
`verified`, `RunSummary` and a canonical final-state byte dump. The `fxp-replay` CLI below uses this same API. The API opens the log read-only, checks versions/configuration and
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

Deliberately deferred: controlling bots' clocks/randomness, UI, UDP
market data, io_uring, pinned thread-per-core, fixed-layout messages, group commit
and journal-backed sessions. No changes to wire schemas or bot logic are made.


## fxp-replay CLI

Build from the repository root:

```sh
cargo build --release --manifest-path fxp_core/Cargo.toml --bin fxp-replay
./fxp_core/target/release/fxp-replay --help
```

`cargo run` still defaults to the core server. Use `--bin fxp-replay` when running
the CLI through Cargo. Arguments must be UTF-8; invalid argument encoding exits 2. All commands are offline and read-only. They load the
bundle into memory and reuse the same injected-clock engine and bundle checks;
JSONL is an output format, not a promise of constant-memory replay.

### Verify and exit codes

```sh
./fxp_core/target/release/fxp-replay verify fxp_core/tests/bundles/partial-fill
# IDENTICAL events=4 symbols=1 global=... state=...
```

| Exit | Meaning |
| --- | --- |
| 0 | `verify`: counts, digests, final state and available recorded trace match; `diff`: equal runs; other commands: success |
| 1 | Valid complete inputs diverge (`verify` or `diff`) |
| 2 | Invalid arguments, I/O failure, tampering, unsupported versions, missing required files, or incomplete `verify`/`diff` input |

Verification runs a fresh engine. On event divergence it prints the first differing
trace row's global command sequence, symbol, decoded input, expected event and
actual event, followed by expected/actual aggregate results. Counterparty IDs in
that diagnostic use anonymous labels. Missing events are represented by null.
If all events match but the final state differs, it reports the state hashes;
there is no fictional differing business event or first state-changing command.

Prompt-4 bundles without a recorded event trace still verify their counts,
business digests and final state. Their expected individual events cannot be
recovered from a SHA-256 digest: if verification fails, the CLI explicitly reports
that the first expected event/sequence is unavailable. It does not run the same
new engine twice and call one result the historical expectation.

### Timeline and ownership

```sh
./fxp_core/target/release/fxp-replay timeline fxp_core/tests/bundles/partial-fill --order B
# ... accepted quantity=10 leaves=10
# ... fill price=100 quantity=4 leaves=6 role=resting counterparty=anon:...

./fxp_core/target/release/fxp-replay timeline fxp_core/tests/bundles/partial-fill \
  --client-order-id c-B --owner alice --symbol TEST --from 1 --to 2 --json
```

`--order ID` and `--client-order-id ID` are alternatives. They follow successful
replacement chains in either direction; rejected replacements do not create
aliases. Aliases are scoped by symbol and owner. If one owner reuses an ID for
multiple order lifetimes, use the sequence range to select an instance. `--owner` and `--symbol` can be combined with either identifier. `--from`
and `--to` are inclusive **global command sequences**, starting at 1. All events
caused by a selected command share its sequence; `event_index` orders receipts,
diagnostics and business events within that command. Commands that trigger other
owners' resting fills still attribute each fill to its own owner. Order-ID reuse
across an expiry/new-order boundary retains the previous event's owner.

The timeline shows receipts, acceptance (including fully aggressive orders),
rejections with their exact reasons, fills with price/quantity and resting,
aggressor or auction role, replacements, stop triggers, slice replenishments,
and cancellation causes. Causes come from execution reports or explicit engine
context: user cancel, STP mode, expiry, mass cancel, cancel-on-disconnect,
auction remainder and unfilled immediate remainder. Missing causes are labeled
`cause not recorded`; they are never guessed. This engine has no peg order type,
so no peg-reprice events are synthesized.

Counterparty order IDs default to stable `anon:` labels. `--owner alice` filters
to Alice's events and enables recorded counterparty IDs for that scope. This is
an offline display filter, not authentication or an access-control boundary.
Bundle files already contain the run's commands for all owners.

Text is the default. `--json` produces an `fxp-timeline-v1` object with `source`,
`verified` and `events`. `source=recorded` uses the integrity-checked live trace;
legacy bundles use `source=replayed`. `command_time` is the admission clock;
`data.business_time` preserves the actual engine timestamp, including expiry or
auction boundaries crossed by a later tick. These times need not be equal.

### Explain, diff and export

```sh
./fxp_core/target/release/fxp-replay explain fxp_core/tests/bundles/replace --order B
# explain source=recorded verified=true
# B: 0 fill reports, 0 units; 0 stop triggers; 0 iceberg refreshes.
# TEST B owner=alice: replaced: new order B2 (first seq 2, count 1).
# TEST B2 owner=alice: replace rejected: negative replace fields are invalid (first seq 3, count 1).
# TEST B2 owner=alice: canceled: user (first seq 4, count 1).

./fxp_core/target/release/fxp-replay diff \
  fxp_core/tests/bundles/partial-fill fxp_core/tests/bundles/reject --json
# Exit 1: first divergence, A/B order and fill counts, rejects grouped by reason.

./fxp_core/target/release/fxp-replay export fxp_core/tests/bundles/iceberg \
  --jsonl --order ICE > iceberg.jsonl
```

`explain` summarizes recorded outcomes and exact cause strings; it can also use
`--owner`, `--symbol` and `--json`. A selected order with no causal outcome gets
an explicit no-cause-recorded summary. It does not claim to explain bot intent.

`diff` compares source events at their global positions plus the recorded
aggregate/final-state expectations. It does not align reordered commands or
normalize recorded clocks, IDs or business timestamps. A timestamp change can
therefore be the first difference. `orders` counts new-order commands (including
rejected submissions); `fills` counts public trade prints, rather than counting
both owners' execution reports twice. Reject counts include request rejections
and are grouped by their exact recorded reason. If current replay diverges from
a recorded reference, the CLI warns while comparing historical source events.

`export --jsonl` accepts the timeline filters and writes one JSON object per
line with the same counterparty display policy. `timeline`, `explain` and
`export` can inspect an incomplete run's replayed committed prefix, with
`verified=false` and a stderr warning. They never call that prefix verified.

### Additive trace and golden tests

New recordings add `events.jsonl`, `trace_version=fxp-trace-v1` and a completed
`trace_sha256` to the existing `fxp-run-v1` manifest. The trace records command
receipts, canonical business bytes and engine-supplied diagnostic context.
Diagnostic context is not transmitted on the wire and is not added to the
canonical business digest or final-state encoding. The command-log format,
replay version, protobuf schema and per-command `sync_data` policy are unchanged.

Trace writes add recording work. Trace data is synced before the completed
manifest is published; a crash leaves the run incomplete and its trace
non-authoritative. Any trace write failure poisons finalization. Completed
trace tampering or an unknown trace version is invalid input (exit 2), not an
engine divergence. The format remains unsigned, as described above.

Small bundles under `fxp_core/tests/bundles/` cover partial fills, STP,
rejection reasons, expiry, auctions, icebergs, mass cancel, stop triggers,
replace chains and disconnect cancels. Text/JSON timelines and explanations
are checked-in snapshots. Tests also cover filters, redaction, JSONL, diff,
first-divergence coordinates, legacy bundles and exit codes. An independent
Python implementation validates event-log protobuf/CRC framing, trace JSON,
canonical frame fields and SHA-256 digests against those fixtures. The existing
required `rust` CI job runs `fxp-replay verify` over every golden bundle.

## Run report

```sh
fxp-replay report /path/to/bundle --html out.html
fxp-replay report /path/to/bundle --html mine.html --json mine.json --owner alice --anonymize-owners
fxp-replay report /path/to/bundle --json window.json --from 10000 --to 20000
```

Open the generated HTML by double-clicking it. It is one self-contained file,
with a console-style dark theme, no server, network calls, fonts, dependencies
or external assets. `--json PATH` emits the same embedded data without HTML;
both formats can be requested together. Output files must be new and outside
the bundle; existing files are refused. Unix output permissions are 0600.

The report shows original bundle provenance and expected/actual digests,
verification status and the first divergence when available, whole-run counts,
per-owner counts, a filterable order table, linked replacement lifecycles and
the same event-derived explanation as `timeline` / `explain`. Rejecting a cancel
or replace does not turn a filled or still-working order into a rejected order.
Orders with successful replacements share one chain; rejected attempts remain
visible in that chain. Summary `orders` counts new commands, `fills` counts
public trade prints, and per-owner `fill_reports` counts private fill reports.
Expiry is included in cancels by cause. There is no visual two-bundle diff.

`--owner NAME` selects working-order ladder marks and the same counterparty
scope as timeline. It **does not hide other owners' orders**; table owner filters
are available in the report. Without owner scope, counterparties and STP opposing
IDs are anonymized by default. `--anonymize-owners` gives non-selected owners
stable labels from sorted identities; all order/client IDs are pseudonymized,
even for the selected owner, so namespaced IDs cannot expose other owners.
Account/STP-group fields and raw protobuf/canonical hex are omitted, and free-form
manifest `meta` and unknown manifest extensions are omitted in anonymized reports. The banner states that owner
anonymization was applied. Labels are stable within a bundle, not an encryption
or de-identification guarantee; financial symbols, prices, quantities, timestamps
and original digests remain. Free-text explanations/reasons are masked as well.

All bundle strings are untrusted. Embedded JSON escapes `<`, `>`, `&` and Unicode
line separators; rendering uses DOM `textContent`, never HTML interpolation.
The CSP allows only the hashes of the shipped static script/style and disables
network, images, objects, frames, forms and base URLs. There are no inline event
handlers. A file can safely display an owner or client ID containing HTML as
literal text. The CSP hashes are checked with an independent Python SHA-256 /
Base64 implementation; browser smoke reuses Chromium already installed for the
console CI job, with offline file URLs and no report server.

### Book boundaries and bounds

The scrubber uses **post-command global journal sequence**, not an intermediate
execution-report index. Matching, stop cascades and auction uncrosses complete
atomically before that boundary's engine snapshot. It shows the actual visible
best 10 bid and ask levels, and the selected owner's displayed/leaves quantities
and order IDs on those levels. Hidden iceberg reserves, untriggered stops,
held auction market orders and working orders outside those levels are not
public ladder levels. The displayed leaves for the selected owner's iceberg can
include its reserve; aggregate ladder quantity is displayed quantity only.

Replay precomputes frames only when a symbol's ladder, marks or phase changes.
Identical frames are coalesced; tape prints are stored once. The scrubber can
select any retained command boundary. Tape shows the last 100 prints up to that
boundary; all window prints are in JSON. Values are fixed-point integer units,
not floating-point prices. Symbol configuration and its hash are in the source
bundle; the report identifies that configuration via manifest provenance.

Limits: at most **256 MiB** of source bundle files, **100,000 retained timeline
rows**, **48 MiB** of book frames, **96 MiB** serialized JSON, and **100 MiB** HTML.
A durable fixture with **50,000 business events** (100,000 command/event rows)
is exercised in CI with a 120-second *generation* test budget, excluding fixture
creation. This is a functional safety bound, not an absolute performance claim.

For larger runs use `--from SEQ --to SEQ`. The entire source is still replayed
and verified; the ladder includes exact prefix state immediately before `from`.
Table lifecycle/tape data is windowed and whole-run summaries remain whole-run.
The initial prefix has no earlier tape prints; there is no claim to display
prints outside the chosen window. The source-file cap still applies, and very
large books can exceed the frame/data cap even in a small window. The generator
fails clearly rather than silently truncating. `FXP_UPDATE_REPORT_GOLDENS=1` is
a test-only opt-in to regenerate checked-in report JSON snapshots; never use it
to bless unexpected changes without reviewing their diff.

Exit codes follow verify: **0** identical, **1** divergence, **2** invalid or
incomplete. Incomplete bundles can produce a conspicuously unverified report;
tampered logs and unknown versions fail before output. Divergent reports show
replayed lifecycles/books alongside the first expected/actual event difference;
legacy aggregate-only bundles cannot identify a missing first event. Neither
report nor replay reproduces bot decisions. `make repro-demo BUG=<name>` also
writes and validates `report.html` / `report.json` and prints their paths.

### Stable JSON contract: `fxp-run-report-v1`

The machine-readable schema is [run-report.schema.json](run-report.schema.json).
Consumers must check `schema`, accept additive keys, and reject unsupported
major schema versions. All integers, including nested timeline fields and
provenance counts, are **decimal strings** to preserve signed 64-bit prices and
unsigned 64-bit sequences in JavaScript. Booleans and null retain their JSON
types; strings are never trusted markup.

| Key | Meaning |
| --- | --- |
| `schema` | Exactly `fxp-run-report-v1` |
| `manifest` | Bundle manifest; free-form meta omitted when anonymized |
| `verification` | `result` (`identical`, `divergent`, `incomplete`), event `source`, first divergence, expected/actual digest summaries |
| `privacy` | `anonymized_owners` boolean, selected owner or null |
| `window` | Inclusive from/to, bounds, atomic-boundary description |
| `summary` | Whole-run new orders, public fills, rejects/cancels by reason/cause and owner counts |
| `orders` | Replacement-chain root ID, aliases, client IDs, owner/symbol, end-of-window status, reasons, explanation, event-position pairs |
| `events` | Windowed timeline rows, all owners; raw canonical/protobuf bytes excluded; numeric values are strings |
| `symbols` | Sorted symbols in the run |
| `books` | Chronological per-symbol changed frames: sequence, symbol and ladder (`bids`, `asks`, phase); optional prefix frames precede the window |
| `tape` | Window public trade rows, ordered by global sequence/event index |

Each order's `positions` is a list of `[global_seq,event_index]` pairs into
`events`; replacements are connected before windowing. Levels carry `price`,
`quantity` and `own_orders` (`order_id`, `displayed`, `leaves`). Report status and
explanations use recorded rows when verified; the book always comes from a fresh
engine replay whose final digest/state summary is cross-checked against the
verification pass. This export is an inspection artifact, not a new wire format
or a signed verification receipt.
