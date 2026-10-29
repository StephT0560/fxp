# Subscriber degradation: work counts and a relative harness test

Harness contention contributes to the observed degradation. Moving the same
readers into a separate process changes completed-order throughput by **1.368×
[1.268, 1.379]**, B/A over five interleaved pairs: **relative, shared hardware**.
There is no per-subscriber protobuf encoding and no measured quadratic fan-out
or write amplification. The gateway does allocate two buffers per subscriber
per incremental update; that is real linear work, not evidence of a measured
production throughput bottleneck.

**One recommendation: use the separate-process subscriber harness; make no
production change on this evidence.** The harness change is implemented here.
This is evidence that cause A is present, not that all or most of the entire
N=0 → N=100 throughput loss is a harness artifact. Quiet-hardware validation
remains the manual step below.

## Scope and provenance

Measured source: `7fdb2a09572b27ebcf30b1690dd4fd03445f5699`, clean working tree.
Later worker-only fixes preserve private TLS options and cumulative startup
deadlines. Those startup paths do not change the measured plaintext update loop;
the archived measurements are attributed to the original SHA, not relabeled as
measurements of a later revision.
[Machine/build metadata](../../benchmarks/phase1-lite/environment.json) and
[independently checked binary hashes](../../benchmarks/phase1-lite/binaries.json): Intel
Xeon Platinum 8370C, shared KVM, three visible CPUs, shared two-CPU cgroup quota;
Rust 1.99.1 and Go 1.26.10, Linux amd64. Live core/gateway are release builds,
plaintext loopback, with recording/event-log persistence **disabled** and
`GOMAXPROCS=1`, `TOKIO_WORKER_THREADS=1`. This does not compare durability modes.
Allocation fixtures use the explicitly gated Rust `phase1`/dhat feature and Go
`phase1` build tag; neither is enabled in the relative timing runs.

Core CPU 0, gateway CPU 1, generator CPU 2. In A the subscribers share the
benchmark's process, Go runtime and CPU 2. In B they use a separate process on
CPU 0, sharing that CPU with the core. Four disjoint CPUs are unavailable here;
the aggregate quota is still only two CPUs. Thus the test removes generator
runtime/CPU sharing but does not isolate every process or establish dedicated
hardware performance.

Absolute latency, elapsed time and orders/s from this environment are **smoke
only**, not results in this report or its checked-in evidence. Raw duration-bearing
pprof/dhat files and benchmark output stay under ignored `work/`. Published
profiles retain allocation counts/bytes and mutex contention counts only; syscall
traces have no timing flags. No absolute timing results or FIX comparison are made.

## Deterministic work-per-update attribution

The isolated attribution workload injects **2,000 identical incremental updates**:
a single PHASE1 level at price 100, quantity 1, buy side; 18-byte protobuf payload,
22-byte length-prefixed frame. This is a transport fixture, not a complete
matching-engine allocation benchmark. It calls the actual core `frame_message` /
`broadcast_md` and gateway `ForwardMarketData` / client writer paths. Each healthy
receiver acknowledges each update before the next injection, so delivered counts
are exactly `2,000 × recipients`, with no loss or undrained tail.

There are 128 unmeasured warmup updates to initialize reflection, stacks and
cyclic channel storage. Core bytes/allocations are dhat HeapStats deltas. Gateway
bytes/allocations are MemStats deltas, corroborated by unsampled
`pprof alloc_objects` and `alloc_space` (sample rate 1). Counter branches allocate
nothing. Go deltas can include a few runtime allocations; the small residuals
are retained rather than rounded away as exact application invariants. Byte
accounting is allocator-specific, not RSS or a cross-language performance ranking.

The normal topology has **one dedicated gateway-to-core MD connection** for any
positive external subscriber count. The core table maps N external subscribers
to zero or one core recipient. This is independently covered by the existing
`TestSubscriptionsUseOneCoreConnection` regression. Snapshot/phase/trade messages
are excluded from the incremental-update denominator. The live order workload
below confirms socket writes for the same message class.

### Work per source incremental update, U

| External subscribers N | Core recipients | Core encodes/U | Core allocations/U | Core allocated B/U | Gateway marshals/U | Gateway allocations/U | Gateway allocated B/U | Gateway enqueue locks/U | Gateway socket Write calls/U |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | 0 | 1 | 5 | 82 | 1* | 5* | 128* | 0 | 0 |
| 1 | 1 | 1 | 6 | 104 | 1 | 7.001 | 176.056 | 1 | 1 |
| 10 | 1 | 1 | 6 | 104 | 1 | 25.0005 | 608.024 | 10 | 10 |
| 100 | 1 | 1 | 6 | 104 | 1 | 205 | 4,928 | 100 | 100 |

`*` N=0 is the deliberately injected empty-fan-out function baseline. A fresh
live stack without any subscription sends no incrementals to the gateway and
therefore does not call its MD marshal path at all. It is not a measurement of
5 gateway allocations for an update that never arrives.

For **per externally delivered update**, divide source-update counts/bytes by N
when N>0; N=0 has no delivered denominator. For example, at N=100 the core encode
and gateway marshal counts are each **0.01 per external delivery**, gateway
allocations are **2.05 per delivery**, allocated bytes **49.28 per delivery**,
and enqueue locks / socket writes are each **1 per delivery**. Fractional core
work is amortization of its single gateway feed, not fractional operations.
Raw integers: [core-gateway-N](../../benchmarks/phase1-lite/core-gateway-100.json)
and [gateway-N](../../benchmarks/phase1-lite/gateway-100.json), with all four N
values and allocation-stack profiles beside them.

The application shape is **5 + 2N gateway allocations per source update**,
with two recipient-specific buffers: the copy in `clientWriter.enqueue` and
the length-prefixed frame in `clientWriter.write`. On this payload/allocator,
bytes follow **128 + 48N**, apart from the recorded runtime residuals.
[pprof allocation objects](../../benchmarks/phase1-lite/gateway-100.alloc_objects.txt)
and [allocation bytes](../../benchmarks/phase1-lite/gateway-100.alloc_space.txt)
attribute the two N-scaled buffer allocations to those actual call sites.
That is twice the allocation slope of one required write, **not O(N²)** work.

To distinguish gateway topology from direct core subscriptions, a second fixture
connects N receivers directly to the core broadcast queue:

| Direct core recipients | Encodes/U | Allocations/U | Allocated B/U |
| ---: | ---: | ---: | ---: |
| 0 | 1 | 5 | 82 |
| 1 | 1 | 6 | 104 |
| 10 | 1 | 15 | 302 |
| 100 | 1 | 105 | 2,282 |

Encoding remains once; `Vec::clone` adds one allocation and 22 bytes per direct
recipient. This alternative topology is not the ordinary external-subscriber
path. The core has no explicit mutex acquisition in its broadcast loop; internal
Tokio channel synchronization is not claimed to be zero-cost or counted as an
explicit application lock.

### Syscalls and lock contention

A separate live workload runs **2,000 paired orders**, 100 completed warmup
orders, one symbol and one order session. It produces **4,000 measured
incrementals**; this update count belongs to this specific paired-order fixture,
not a general protocol promise. Subscriber readiness precedes measurement and
all expected incrementals drain after the completed-order timer ends.

`strace -f -yy -xx -s 8` records write-family calls in each process. The parser
selects framed TCP incremental envelope tag 10, excludes files/stdio and other
payload types, and accounts for resumed calls, errors and short writes. These
are syscall counts from an instrumented smoke, not strace timing results.

| N | Core incremental write syscalls | Gateway incremental write syscalls | Writes per delivery at either hop | Errors / short writes |
| ---: | ---: | ---: | ---: | ---: |
| 0 | 0 | 0 | undefined | 0 / 0 |
| 1 | 4,000 | 4,000 | 1 | 0 / 0 |
| 10 | 4,000 | 40,000 | 1 | 0 / 0 |
| 100 | 4,000 | 400,000 | 1 | 0 / 0 |

[Raw counts](../../benchmarks/phase1-lite/live-syscalls.json) and compressed,
untimed syscall traces are checked in. The separate Go fixture also checks
one `net.Conn.Write` per delivered incremental; its trace includes 128 warmup
updates, so its kernel totals are `(2,000 + 128) × N`, not `2,000 × N`.

Gateway enqueue `RWMutex.RLock` acquisitions are exactly N/U. The
[mutex contention-count profiles](../../benchmarks/phase1-lite/gateway-100.locks.txt)
show no contended enqueue lock in this fixture. They can contain runtime,
network or teardown locks, and do not establish absence of contention under
other workloads. Mutex delay totals are deliberately not published.

## Relative contention comparison

A,B,A,B… for **five pairs**, each run a fresh stack, 2,000 measured orders,
100 warmup orders and 100 subscribers. Both modes use the same subscriber
implementation, protocol decoding, update accounting and readiness. Every run
asserts 2,000 ACK/final-fill samples and **400,000 incremental deliveries**.
The order endpoint includes final-fill drain; the optional MD completion drain
happens after that timer, identically in both modes.

The outer five-pair loop is the repetition unit: each invocation uses existing
`--mode closed --pacer sleep --subscribers 100 --repetitions 1` flags, rather
than five nested repetitions that would break the requested A,B interleaving.
There is no profiler, strace, compiler or parallel test workload running during
these pairs. Only ratios are published.

| Pair | Completed orders/s ratio B/A — relative, shared hardware |
| ---: | ---: |
| 1 | 1.268 |
| 2 | 1.298 |
| 3 | 1.372 |
| 4 | 1.379 |
| 5 | 1.368 |
| **Median [min, max]** | **1.368 [1.268, 1.379]** |

[Ratio-only evidence](../../benchmarks/phase1-lite/contention.json).
All pairs favor separation; the median excess over 1 exceeds the full observed
ratio spread. A whole-set [spread check](../../benchmarks/phase1-lite/normalized-spread.json)
also has disjoint A/B ranges when both are normalized to the A median; the
median difference exceeds each mode's run-to-run spread. This is a measurable
relative effect for this shared placement,
not an absolute throughput claim or an estimate of recovery of the whole N=0
baseline gap. CPU 0 sharing and the two-CPU quota constrain causal interpretation.
Exploratory runs overlapping profiling/tests, and a failed readiness run, were
discarded; their absolute timing output remains ignored smoke only.

## Slow-consumer functional check

| Boundary | Queue depth/capacity | MD drops | Retained queued payload | Heap change after 10,000 further full-queue attempts | Independent order path |
| --- | --- | ---: | ---: | ---: | --- |
| Core queue, receiver parked | 16/16 | 10,984 before orders; 12,986 after orders | 2,048 B | 0 B (dhat current heap) | 2,000 actual engine ACKs |
| Gateway writer, peer never reads | 16/16 | 10,984 | 128 B | -808 B (Go heap after GC) | 2,000 routed execution reports delivered |

[Core counters](../../benchmarks/phase1-lite/core-slow.json) and
[gateway counters](../../benchmarks/phase1-lite/gateway-slow.json).
The core uses a fixed 128-byte test frame; the gateway uses an 8-byte minimal
encoded update. These are capacity checks, not a cross-language footprint
comparison. Core checks the actual bounded channel; the gateway blocks an actual writer on
`net.Pipe` while routing reports through a different healthy connection.
Assertions require drops, full but bounded queues, all order-path completions,
zero retained growth in the isolated core drop loop, and bounded gateway heap
after GC. One in-flight writer frame and channel/runtime bookkeeping are outside
the queued-payload column. Legitimate book/order-state allocations are not
confused with retained subscriber payload: the core heap snapshots bracket the
drop loop before its order-path check. Dropped messages still incur temporary
copies; this is not a zero-allocation claim or a long-duration RSS/kernel-buffer soak.

## Diagnosis and confidence

| Candidate | Finding | Confidence and limit |
| --- | --- | --- |
| A: harness contention | Separating readers improves the paired ratio in every pair | Strong for a contribution on this VM/placement; cannot quantify the fraction of the total N=0 loss or generalize to four dedicated CPUs |
| B: core per-subscriber encoding | One encode/U, including the direct-core N=100 stress | High for this path; normal gateway topology has one core recipient |
| C: gateway fan-out | One marshal/U; N locks, two recipient buffers, N writes/U | High for the linear work and copy attribution; no independent production throughput-bottleneck claim |
| D: syscall amplification | One successful syscall per delivered incremental at each hop | High for this healthy, small-frame smoke; not a claim for TLS, large frames, backpressure or every OS |

There is no measured quantity with quadratic subscriber amplification. The
avoidable production work is C's two recipient buffers; its existence alone
does not justify a production optimization or a speedup claim. The single
recommendation remains the corrected harness, with no production change.
All retained server changes are counters behind test cfg/features or compile-time
build tags; no order behavior, wire format, queue policy or durability changes.

## Reproduce and rerun on quiet hardware

Linux, Rust/Go toolchains, protoc, `strace`, `taskset`, Python 3 and normal build
prerequisites are required. Ports 8080–8082 must be free. Builds precede
measurement; do not overlap these commands with other profiling/build/test runs.

```sh
# From the repository root. Scratch outputs keep source provenance clean.
python3 scripts/phase1_lite.py --fixtures-only --output work/phase1-evidence
python3 scripts/phase1_syscalls.py --output work/phase1-evidence
python3 scripts/phase1_lite.py --pairs-only --output work/phase1-evidence
```

**Manual step:** on quiet hardware with at least four available physical cores,
inspect `lscpu -e=CPU,CORE,SOCKET` and select four non-sibling CPUs. For example,
if 0,2,4,6 are suitable:

```sh
taskset -c 0,2,4,6 python3 scripts/phase1_lite.py --pairs-only --output work/phase1-quiet
```

The runner chooses the first four allowed CPUs for core, gateway, generator and
subscribers; with only three it records the unavoidable core/subscriber sharing.
Retain source SHA, hashes/build settings, quota/affinity and all five pairs;
compare ratios and spread before deciding whether any production work is needed.
`--no-build` is only for reusing binaries already built from that same source.
Do not publish this environment's smoke timings as quiet-hardware results.

The existing runners remain usable. `scripts/bench.sh ... --smoke` and
`scripts/profile_r4.py --suite fanout --subscriber-process --subscriber-cpu 0`
were exercised successfully; their absolute outputs stay under `work/` only.
On four-CPU hardware use subscriber CPU 3 with profile_r4's existing core=0,
gateway=1, generator=2 layout, after checking topology.

### Harness and diagnostic switches

- Benchmark `--subscriber-process`: re-exec the benchmark binary as a reader
  worker; default off. Readiness is signaled only after a snapshot for every
  requested symbol, tolerating phase messages and duplicate snapshots.
- `--subscriber-cpu`: optional Linux `taskset` CPU list; requires process mode.
- `--subscriber-drain-updates`: expected measured incrementals per reader;
  default 0 disables the optional check. For this workload use **4000**.
  A missing delivery fails, and both modes close/join all readers. Child readiness,
  control, errors, counts and termination are carried over private pipes. TLS trust,
  certificate/key paths and server name use a worker-only transfer type so the
  public report stays redacted. Startup allows the cumulative serial per-reader
  dial/readiness deadlines rather than one shared reader deadline.
- Rust `--features phase1` activates the isolated dhat fixtures/counter under
  `cfg(test)`; ordinary server builds have no encode counter.
- Go `-tags phase1` activates test-only atomic encode/lock/write counters.
  The normal build uses a constant-false guard, so counter branches are compiled
  out. No new operational metrics endpoint is introduced.
- Fixture-only `FXP_PHASE1_SUBSCRIBERS` selects 0/1/10/100 receivers;
  `FXP_PHASE1_OUTPUT` selects a local JSON output path. Raw profiles are kept
  locally; only count/byte summaries are published.

Normal benchmark flags, default in-process mode and order timing endpoints remain
available. New tests cover process configuration, worker control, complete drain,
missing updates and phase/duplicate-snapshot readiness. Existing CI Rust/Go jobs
also run the gated attribution/slow-reader fixtures; no new CI job or dependency
name is added. Production code changes are limited to compile-time gated counters.
