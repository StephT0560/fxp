# Benchmark contract

## Live gateway/core harness

`go run ./benchmark` in `go_gateway/` connects to a separately running TCP
gateway and Rust core. Use an isolated fresh stack. Each connection alternates
BUY/SELL limit GTC orders at the same price and quantity, with STP explicitly
NONE; each phase has an even order count and waits for every order to fill.
Adjacent connection pairs share a symbol in spread mode; single mode uses one
symbol. No external orders or auction schedules may interfere with the fixture.

There are two distinct endpoints: **ACK** is EXEC_NEW, and **fill** is a terminal
ORDER_FILLED report. Acknowledgments are never counted as fills, and each order
contributes exactly one sample to each histogram. Throughput is completed
orders/second, including final fill drain, not reports/second or trades/second.
One trade normally fills two orders. Fill latency includes the workload's wait
for the opposite-side order; it is not interchangeable with acknowledgment
latency or a claim about bare matching time. Market-data fan-out is not measured;
the old uncorrelated "next update" probe has been removed.

`--mode open --rate R` schedules each connection's order i at
`phase_start + i/R` using a common monotonic start. The sender never advances
that schedule when late and never uses a ticker that can discard ticks. Latency
starts at that **intended** time, before message construction, serialization,
blocked writes and scheduling delays. `max_send_lag_ns` exposes lateness immediately before constructing each order;
latency also includes that order's serialization and blocked write. This
accounts for coordinated omission in this finite prescribed workload; it cannot
infer traffic after the requested order count or make an overloaded run a
steady-state capacity estimate. It does not apply an additional synthetic HDR
"correction" on top of the already-correct intended timestamps.

`--mode closed` allows one outstanding ACK per connection. Its start time is the
actual next permitted send; no open-loop/queueing claim is made. `--rate` is
ignored in this mode. Its deadline refreshes for each send; a healthy phase may
last longer than the response timeout. Open mode retains its fixed deadline
relative to the intended end of the phase. Paired orders may overlap until both
final fills arrive.
A completed separate warmup phase on the same connections precedes measurement.
Each repetition reconnects and completes its own warmup; default **5**
repetitions. All counts are exact, warmup samples are excluded and run summary
statistics are the median and min/max of the **per-run** percentiles/rates,
not percentiles of a pooled distribution. Even-N medians average the middle two.

HDR histograms have 3 significant digits and a 1 ns–24 hour range. JSON includes
per-run HDR snapshots, sample counts, p50/p90/p99/p99.9/max in ns, elapsed time,
throughput, environment, symbol rules and actual encoded minor-unit prices.
HDR extrema are quantized, not raw nanosecond precision. A p99.9 from a small
sample is essentially an extreme and has weak statistical power. Rejections,
missing ACKs/fills, bad frames, duplicate endpoint reports and histogram overflow
fail the run rather than silently dropping slow observations. Failed JSON has an
error and no success summary; its Markdown output is marked failed.

The default price is **$3000.00 = 300_000** units at scale 2. `FXP_SYMBOL_CONFIG`
is loaded exactly as for the bridge; configured scales rescale that decimal,
and the harness validates tick, lot and min/max quantity before connecting.
All three processes must use compatible rules. `--quantity` is integral and
must comply with those rules. Symbol definitions/prices are included in JSON.
Authentication uses `FXP_JWT_SECRET` (dev fallback only when unset),
`FXP_JWT_ISSUER`, `FXP_JWT_AUDIENCE` and `FXP_JWT_KID` through the shared dev JWT
helper, with fresh jti per connection. Secrets/tokens are never printed.

Environment includes CPU model, visible CPUs, Go version/GOMAXPROCS, local
rustc version, client affinity, explicit pinning notes and relevant launch env.
The local compiler is not proof of a remote core's build compiler. The supplied
runner builds locally and records build versions and component affinities.

## Encoding fixtures

Criterion benchmarks exclude fixture construction, networking, matching,
authentication, session recovery, encryption and full FIX dictionary validation.
Allocation variants are named. `FIX_format` retains the allocating formatting
baseline; `FIX_preallocated` reuses body/frame buffers and uses integer decimal
conversion with no format!/floating point. Reused buffers are warmed and every
iteration exposes the encoded bytes to black_box. SBE encoder inputs and outputs
are opaque too; returned strings and numeric fields are all consumed by the sink.

The corrected NewOrder fixture includes DAY TIF and a timestamp in FXP and the
local SBE layout as well as FIX. SBE is a **hand-written fixture layout**, not a
generated exchange-certified SBE implementation. Full owned SBE decode allocates
strings and reads all fixture fields; it no longer measures only four scalar
loads. FIX decode still builds an owned generic tag map rather than a typed,
optimized production parser. This is a comparison of these implementations and
allocation strategies, not a universal protocol speed ranking. The roundtrip
group executes both operations in one timed iteration rather than adding two
separately measured medians. Secondary report/snapshot/incremental fixtures have
different field subsets and must not be used to claim semantic equivalence.

## Limits on interpretation

Measurements run on a shared VM over loopback, with client/gateway/core sharing
one host. CPU contention, virtualization, frequency scaling, scheduler pauses,
GC, connection-pool size and kernel buffers affect results. Explicitly record
pinning or its absence. These are neither WAN numbers nor exchange capacity,
production SLOs or independent confirmations of historical Windows figures.
Report slower results and full run spread. No automated regression threshold is
justified yet; nightly runs archive evidence for manual comparison.

## Round-4 pacing attribution

`--pacer sleep|busy-spin|hybrid` selects deadline waiting without changing the
absolute open-loop schedule. The default remains `sleep`; hybrid sleeps until
100 microseconds before the deadline and then spins. Busy-spin can consume an
entire generator CPU and compete with its receiver; record CPU quota as well as
affinity. It is an experiment, not a production tuning recommendation.

Each ACK now has paired `send_lag` (intended to pre-construction send) and
`ack_after_send` (pre-construction send to receive) histograms. The latter still
includes construction, marshal and blocked write, not just server time. These
sample durations add to intended ACK latency, but their independently computed
percentiles do **not** add; do not subtract p50s to estimate a causal fraction.
`max_send_lag_ns` retains its original meaning. Attribution is available for
closed mode too, where no predetermined arrival schedule exists.

## Read-only console evidence schema (v1)

`benchmarks/console.json` is a deterministic projection of the five primary R4
pacing/closed scenarios. `python3 scripts/export_console_bench.py` regenerates it;
`--check` verifies byte-for-byte agreement with the original `.json.gz` files and
their SHA-256 digests. It copies all configuration, environment, per-run endpoint
statistics and summaries verbatim, removing only bulky per-run HDR snapshots.
It never runs a workload, rounds values, pools distributions or subtracts
percentiles. The full HDR evidence remains in `benchmarks/round4/`.

| Field | v1 contract |
|---|---|
| `schema_version` | Integer `1` |
| `profile` | `/api/bench/profile-r4`, serving the checked-in profiling report |
| `scenarios` | Nonempty array; unique nonempty `id` |
| `source`, `source_sha256` | `benchmarks/round4/<id>.json.gz`; lowercase 64-hex digest of the compressed artifact |
| `git_sha`, `created_utc` | Measured revision (40 lowercase hex, matching `environment.revision`); RFC3339 measurement time |
| `network`, `protocol` | This dataset accepts only `loopback`, `FXP`; other populations require a separate schema/dataset and view |
| `config` | Original harness config, including `mode` (`open`/`closed`), nonempty `pacer`, `pinning_notes`, integer `repetitions >= 2` |
| `environment` | Original string map; nonempty `cpu`, `os`, `arch`, `go`, `rustc_local`, `revision`, `pinning_notes` required |
| `runs` | Exactly `config.repetitions` original per-run records, excluding HDR snapshots; positive endpoint sample counts |
| `summary` | Original map of `{median,min,max}` finite nonnegative numbers, with `min <= median <= max` |

Required chart metrics are `ack_p50_ns`, `ack_p99_ns`,
`ack_after_send_p50_ns`, and `ack_after_send_p99_ns`. The BFF checks their spreads
against the per-run `ack` / `ack_after_send` percentiles, including even-N medians.
Missing fields, null values, unsupported populations, duplicate ids, invalid
metadata, or inconsistent summaries make the complete document unavailable
(HTTP 503). The browser validates again before converting ns to ms. It charts
one scenario at a time with asymmetric error bars and an accompanying numeric
median [min–max] table. Compiler metadata is explicitly local compiler evidence;
it must not be upgraded into a claim about an unverified remote core build.

These records describe the measured historical build, not the current BFF
binary or live browser traffic. No capacity metric is inferred from these five
scenarios. Any capacity discussion uses **tested knee**, references the finite
load and environment in R4, and never promises an SLO or ranks protocols.
