# Subscriber isolation: retain the corrected harness

**Recommendation implemented: retain the separate-process subscriber harness;
no production optimization.** The [merged 1-lite baseline](phase1-lite.md)
identified harness contention: separate readers changed completed-order throughput
by 1.368× [1.268, 1.379], relative, shared hardware. It did not establish that
most or all subscriber degradation was a harness artifact. It found one encode
per update in each process, linear buffer allocation and one write per recipient,
not per-recipient encoding or nonlinear write amplification. The requested
`phase1-baseline.md` filename did not exist on main; this branch adds an explicit
[index](phase1-baseline.md) to that existing report instead of inventing a new
baseline diagnosis.

Queue behavior already satisfies the bounded-message policy. This change adds
regressions, collection scripts and delivery documentation. It changes no
production code, wire format, timer, FIX path or queue capacity. The original
separate-process harness remains available through `--subscriber-process` and
`--subscriber-cpu`, including TLS and authenticated startup.

## Method and provenance

Before: main `663cb26196eb528223d03ebb828fab427fa91f50`.
After: `b67ba992e51f08295d64988aee3e863fbcb18b4b`.
Later report/collector commits, including fresh-checkout binding generation,
are not relabeled as measured revisions. Build
both revisions independently, copy executables into separate directories, and
use **one benchmark executable built from the after revision for both sides**.
Tracked sources were committed before building. The environment JSON's dirty
flag is sampled at the end and includes newly collected untracked evidence and
the additional slow-count collector, not an uncommitted production edit.

Shared KVM Intel Xeon Platinum 8370C; three visible CPUs, shared two-CPU quota
(`cpu.max = 200000 100000`); Rust 1.99.1, Go 1.26.10, Linux amd64. Core CPU 0,
gateway CPU 1, generator CPU 2, separate subscribers CPU 0 (sharing with core
because a fourth CPU is unavailable). `GOMAXPROCS=1`,
`TOKIO_WORKER_THREADS=1`. Live Rust release / Go normal builds; plaintext
loopback; event log and run recording disabled. No durability-mode comparison.
No profiling, builds or test suites overlapped the live paired measurement.

For each N=0,10,100: five before/after pairs, alternating before then after,
fresh stack for every half, same corrected harness, closed-loop concurrency 1,
sleep pacer, 2,000 paired orders and 100 completed warmup orders. Every run
completed all 2,000 orders and drained exactly 4,000×N incrementals before
teardown. Outer pairing supplies five repetitions (`--repetitions 1` per half).
Absolute times, latency histograms and orders/s remain ignored smoke output
under `work/`; only ratios and counts are published.

The independent allocation/counter fixture is unchanged from 1-lite: 128 warmup
then 2,000 identical 18-byte protobuf / 22-byte framed incrementals, healthy
readers synchronously drained. It uses gated `phase1` counters and dhat for Rust,
Go MemStats for allocations, and exact marshal/write/enqueue-lock counters.
Each revision has five fixture repetitions at each N. Normal gateway topology
means one core MD recipient for positive external N. See the baseline for the
independent syscall traces and allocation profiles; this follow-up does not
reinterpret net.Conn.Write counters as new kernel syscall measurements.

## Before/after work per source update

All entries are median [min, max] over five repetitions. Exact counts shown as
`x` mean `x [x, x]`. Source update denominator U=2,000; for per-external-delivery
counts divide by N when N>0. No delivered denominator exists at N=0.

| N | Revision | Core encode/U | Core alloc/U | Core B/U | Gateway marshal/U | Gateway enqueue locks/U | Gateway Write calls/U |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | before | 1 | 5 | 82 | 1* | 0 | 0 |
| 0 | after | 1 | 5 | 82 | 1* | 0 | 0 |
| 10 | before | 1 | 6 | 104 | 1 | 10 | 10 |
| 10 | after | 1 | 6 | 104 | 1 | 10 | 10 |
| 100 | before | 1 | 6 | 104 | 1 | 100 | 100 |
| 100 | after | 1 | 6 | 104 | 1 | 100 | 100 |

`*` Forced empty-fan-out function baseline. A fresh live stack with no
subscriptions receives no gateway MD and performs no gateway MD marshaling.

| N | Revision | Gateway alloc/U | Gateway allocated B/U |
| ---: | --- | ---: | ---: |
| 0 | before | 5 [5, 5] | 128 [128, 128] |
| 0 | after | 5 [5, 5] | 128 [128, 128] |
| 10 | before | 25.0005 [25, 25.0005] | 608.024 [608, 608.032] |
| 10 | after | 25.0005 [25, 25.0015] | 608.032 [608, 608.080] |
| 100 | before | 205 [205, 205] | 4928 [4928, 4928] |
| 100 | after | 205 [205, 205] | 4928 [4928, 4928] |

The small Go residuals include runtime allocations. There is no measured change
in application work; the two per-recipient gateway buffers remain. Full integer
measurements: [counts.json](../../benchmarks/subscriber-isolation/counts.json).

## Relative completed-order throughput

| N | After / before, median [min, max], five interleaved pairs |
| ---: | ---: |
| 0 | 0.992× [0.926, 1.077] |
| 10 | 0.975× [0.918, 1.142] |
| 100 | 1.002× [0.977, 1.067] |

**Relative, shared hardware. No measurable effect** at any N: all pair-ratio
ranges include 1 and the median differences are smaller than the observed
spread. This is a no-production-change comparison using the same corrected
harness, not another measurement of old versus corrected harness placement.
It neither claims dedicated-hardware performance nor compares against FIX.
[All pairs](../../benchmarks/subscriber-isolation/relative.json).

## Stalled reader: functional isolation and bounded retention

Before implementation, [declare](phase1-baseline.md) a 4× median stalled/control
elapsed ratio ceiling, deliberately generous relative to the baseline's maximum
1.379× harness effect and 0.128 normalized spread. This is a functional check,
not a p99 limit or latency SLO. One process/session runs five back-to-back
control/stalled pairs, each half delivering 2,000 real routed private reports
while injecting the same update workload. A net.Pipe peer never reads: one
writer is deterministically blocked regardless of socket-buffer sizes. Test
with the race detector; serialize ratios only.

Observed stalled/control slowdown **1.094× [0.955, 1.135]**, below the 4× ceiling.
All 20,000 measured healthy reports arrived. In each stalled half, depth stayed
16/16, exactly 2,983 of 3,000 initial/measurement MD attempts were dropped
(16 queued + one in flight). Another 10,000 drops per pair could not grow the
queue. After five created/closed stalled writers and GC, heap growth was
**1,312 bytes**, below the 2 MiB fixture allowance. Goroutines were 2 before /
3 after: the one additional healthy writer intentionally remained alive until
test cleanup; every stalled writer's finished channel was joined explicitly.
[Raw functional evidence](../../benchmarks/subscriber-isolation/slow-consumer.json).

The unchanged slow-work fixtures also ran five alternating repetitions at each
revision. Exact values have zero spread unless shown:

| Metric | Before | After |
| --- | ---: | ---: |
| Core MD depth / capacity | 16 / 16 | 16 / 16 |
| Core drops before / after 2,000 actual engine ACKs | 10,984 / 12,986 | 10,984 / 12,986 |
| Core queued payload bytes | 2,048 | 2,048 |
| Core retained-byte growth after saturation | 0 [0, 0] | 0 [0, 0] |
| Gateway MD depth / capacity | 16 / 16 | 16 / 16 |
| Gateway drops / routed private reports | 10,984 / 2,000 | 10,984 / 2,000 |
| Gateway queued payload bytes | 128 | 128 |
| Gateway heap growth after saturation+GC, B | -1848 [-1960, -1848] | -1960 [-1960, -1944] |

Core uses a fixed 128-byte frame; gateway uses an 8-byte minimal MD payload.
Queued payload excludes bookkeeping and one in-flight frame. These are not
cross-language memory comparisons. GC deltas may be negative; they are not
claims that dropping frees memory per message. Details:
[slow-counts.json](../../benchmarks/subscriber-isolation/slow-counts.json).

## Regression coverage and limits

- Rust `subscriber_queue_policy_is_bounded_and_signals_critical_overflow`:
  10,000 exact drops, capacity 1, preserved oldest frame, resumed delivery,
  closed-receiver removal, critical-overflow disconnect notification.
- Rust existing `dropped_market_data_can_leave_a_trade_sequence_gap`,
  `slow_market_data_subscriber_does_not_block_other_orders`, and actual TCP
  `tcp_slow_consumer_probe_keeps_healthy_order_latency_bounded` retain engine
  and socket integration coverage. Their diagnostic absolute timing output is
  smoke only and is not a published result here.
- Go `TestSubscriberDropLeavesDetectableTradeGap`: bounded queue, drop-newest,
  then observed public sequence 1,2,4 after a dropped 3.
- Go `TestCriticalOverflowTriggersCancelOnDisconnect`: authenticated native
  handler, stopped reader, critical overflow, socket closure, owner-scoped
  disconnect mass cancel reaches the fake core, writer exit.
- Go `TestSubscriberIsolationRelative`: same-session ratio ceiling, exact
  depths/drop counts, retained heap, joined writers, goroutine leak allowance.
  Runs in the existing Go CI job; no new job or ci-success dependency needed.

Confidence is high in the fixed-workload queue policy and linear-work counts;
the relative result is limited to this placement and workload. It does not
prove that harness contention explains the entire subscriber loss. Strict
whole-process byte bounds, unlimited connection/book growth, universal
book-delta gap detection, historical MD recovery and durable disconnect
cancellation are not claimed. Only trade prints have a usable public gap
sequence; level-only losses remain undetectable by sequence. See
[delivery guarantees](../market-data.md#subscriber-backpressure-and-remaining-limits)
and [operations](../operations.md#subscriber-isolation-audit).

## Manual rerun on quiet hardware

From a clean checkout with the normal Rust/Go/protoc tools available:

```sh
python3 scripts/subscriber_isolation.py --before 663cb26196eb528223d03ebb828fab427fa91f50 --output work/isolation-quiet
python3 scripts/subscriber_isolation_slow.py --output work/isolation-quiet
```

Run once per worktree: the script refuses to overwrite its before-source
worktree on another build. `--no-build` reuses the existing prepared binaries;
use it only without changing either measured revision. On four available
CPUs the script chooses disjoint core/gateway/generator/subscriber CPUs;
restrict affinity to four separate physical cores first where possible.
Record CPU topology/quota, durability, builds and actual SHAs. Keep raw absolute
measurements as local smoke unless a later task authorizes publishing them.

## Local validation

`make fmt-check lint test` passed formatting, clippy and vet, then stopped at
the unchanged repository Go lint baseline: 40 displayed findings (35 errcheck,
2 staticcheck, 3 unconvert). `make test` passed separately. Explicit `cargo test`
passed 241 tests; all three Go modules passed `go vet ./... && go test -race
-count=1 ./...`. Feature-enabled clippy, the gated fixtures and the Python
syscall-parser/port-guard tests passed. Proto generation and the local e2e
suite passed before opening the PR; the PR records CI.
No lint baseline was silently changed or unrelated production fix included.

A review regression checks that both source directories generate their own
ignored Go bindings before the first benchmark build. The test fails against
the previous collector and passes after the startup fix. A fresh detached
checkout independently failed to build without bindings, then successfully
generated them and built the benchmark. This startup-only fix does not change
the already measured update loop or source attribution.
