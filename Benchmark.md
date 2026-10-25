# Corrected benchmark results and methodology

Measurements: 2026-10-25, source **`f0edb3eda1719506366a0835070ddb3cbb52641b`**.
The previous Windows/round-1/round-2 tables are superseded; they used different
hardware, incomplete SBE decoding, unequal encoding allocation and actual-send
latency. Historical reports remain in git history. No speedup claim is carried
forward without an equivalent definition and measured implementation.

## Reproduction and environment

```bash
GOMAXPROCS=2 TOKIO_WORKER_THREADS=2 RUST_LOG=warn bash scripts/bench.sh target/bench
```

The recorded run also verified non-default FXP_JWT_SECRET/ISSUER/AUDIENCE/KID
against the gateway (a disposable verification key, not a production secret).
Raw provenance is in [benchmarks/round3](benchmarks/round3/).

| Environment | Recorded value |
|---|---|
| Host | shared Linux VM, loopback, single host |
| CPU | AMD EPYC 7763; 5 visible logical CPUs |
| Affinity | core, gateway and runner all allowed CPUs 0–4; no explicit pinning |
| Rust | 1.99.1 (58cf8f9c3, 2026-10-13), release profile |
| Go | 1.26.10, go build -trimpath, GOMAXPROCS=2 |
| Core | TOKIO_WORKER_THREADS=2; persistence disabled; no session schedule |
| Gateway | rate=2000/s/connection, burst=4000; default pool=visible logical CPUs |
| Logging | RUST_LOG=warn; normal gateway logging |
| Symbols | unlisted BENCH/BENCH0…4, scale 2, tick 1, quantity 1, price 300000 |
| Encoding | Criterion 0.8.2, 50 samples, 1 s warmup, 2 s target measurement/group |

The runner stops live processes before encoding measurements and uses a fresh
Criterion directory. No other agent builds/tests ran during this recorded run.
The VM itself is shared; this does not eliminate external contention.

## NewOrder serialization results

| Operation / implementation | Median ns [95% CI] |
|---|---:|
| FXP encode, reused buffer | 149.3 [148.9, 149.6] |
| FXP encode, fresh allocation | 292.5 [291.1, 294.2] |
| FIX encode, reusable integer formatter | 133.8 [132.8, 134.8] |
| FIX encode, allocating format! baseline | 535.3 [531.2, 541.1] |
| SBE encode, stack | 29.6 [29.1, 31.7] |
| SBE encode, heap | 36.2 [35.8, 36.8] |
| FXP decode, owned schema | 377.2 [372.6, 385.9] |
| FIX decode, owned generic tag map | 1938.5 [1928.4, 1947.8] |
| SBE decode, owned full fixture | 110.2 [109.7, 111.1] |
| FXP measured encode+decode | 535.1 [532.0, 541.2] |
| FIX measured encode+generic-map decode | 2150.7 [2141.9, 2172.6] |
| SBE measured stack encode+owned decode | 122.1 [121.3, 122.4] |

**The reusable FIX encoder is faster than FXP encode in this run**
(133.8 versus 149.3 ns). The allocating FIX baseline is much slower,
so the old headline based only on format! overstated the advantage. SBE heap
(36.2 ns) is now slower than stack (29.6 ns); full owned SBE decode
is 110.2 ns, not the old ~1 ns scalar projection.
Roundtrip rows execute both operations together; they are not sums of medians.

These are **implementation/fixture** timings. FIX decode is a generic owned tag
map without full session/dictionary validation; SBE is a local hand-written
layout with full owned strings, not a certified exchange-generated implementation.
Differences in decoder design and field handling prevent a universal protocol
ranking. Fixture construction and UTC string conversion are outside timing.
The allocating FIX formatter uses floating point; its output is checked against
the integer encoder for the benchmark fixtures, not all possible int64 prices.

NewOrder fixture sizes are **FXP 94 B, FIX 164 B, SBE 95 B**, including the
NewOrder timestamp and DAY TIF. Secondary fixtures retain their own subsets:
ExecutionReport 91/192/76 B, 5-level snapshot 134/331/168 B (SBE size estimate
only), incremental 32/102/24 B. These subsets are not semantically identical and
do not justify a general compression claim.

## Live paired-order results

Each connection alternates equal-quantity buy/sell limits at $3000.00; every
phase is even and completes all fills. There are **five repetitions** per scenario,
each with 100 separate warmup orders/connection and 1000 measured orders/connection.
Open-loop scenarios offer 1000 orders/s/connection from absolute intended times.
The c10 spread scenario shares five books across adjacent connection pairs.
Closed c1 waits for each ACK before sending the next order; it is labeled
**closed-loop**, not a coordinated-omission-corrected capacity estimate.

| Scenario | Orders/run | Orders/s median [min, max] | ACK count/run | Final-fill count/run |
|---|---:|---:|---:|---:|
| open-c1 | 1,000 | 999.7 [999.1, 1000.0] | 1,000 | 1,000 |
| open-c10 | 10,000 | 9969.5 [9962.2, 9977.3] | 10,000 | 10,000 |
| closed-c1 | 1,000 | 3901.1 [3578.2, 4435.4] | 1,000 | 1,000 |

All latency cells below show **median of the five per-run values [min, max]**,
in milliseconds. ACK means EXEC_NEW; fill means terminal ORDER_FILLED. Every
order contributes one sample to each endpoint; reports are not counted as orders.

| Scenario / endpoint | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms |
|---|---:|---:|---:|---:|---:|
| open-c1 / ack | 0.777 [0.764, 0.796] | 1.281 [1.232, 1.313] | 1.537 [1.415, 1.641] | 2.658 [1.924, 4.370] | 3.332 [1.935, 5.255] |
| open-c1 / fill | 1.337 [1.332, 1.352] | 2.187 [2.179, 2.232] | 2.466 [2.413, 2.585] | 3.961 [2.957, 5.267] | 4.180 [3.068, 6.291] |
| open-c10 / ack | 0.961 [0.935, 0.975] | 1.768 [1.712, 1.800] | 2.542 [2.226, 5.698] | 4.682 [2.841, 13.230] | 5.501 [4.149, 15.262] |
| open-c10 / fill | 1.688 [1.650, 1.734] | 2.644 [2.587, 2.673] | 3.334 [3.125, 5.714] | 5.599 [3.817, 13.591] | 6.627 [4.866, 15.630] |
| closed-c1 / ack | 0.226 [0.191, 0.242] | 0.312 [0.290, 0.330] | 0.453 [0.398, 0.655] | 1.097 [0.888, 2.017] | 1.871 [1.204, 5.374] |
| closed-c1 / fill | 0.353 [0.323, 0.381] | 0.606 [0.520, 0.612] | 0.809 [0.743, 1.351] | 1.411 [1.070, 2.486] | 1.917 [1.569, 5.747] |


For example, c10 open-loop ACK p99 ranges from
2.226 to 5.698 ms;
this spread is published, not discarded as an outlier. The maximum observed
run's terminal-fill tail is 15.630 ms.
A 1000-order run's p99.9 is an extreme with little statistical confidence.

## Initial record retained

The first complete five-repetition dataset (source `dff2cc3`) is preserved in
[benchmarks/round3/initial](benchmarks/round3/initial/), including all raw HDR
snapshots, estimates, provenance and checksums. It measured FXP/FIX reused encode
at 163.6/127.9 ns and owned SBE decode at 114.2 ns. The current tables above come
from a fresh full run after the closed-loop deadline correction (`f0edb3e`),
not a filtered subset of the first run. All measured phases in the initial run
finished within the old timeout; different code revisions/runs are not pooled.
Shared-host variation and the deadline-refresh work preclude assigning every
number change to the fix. Neither favorable nor unfavorable results are erased.

## Caveats and boundaries

- Shared VM, loopback, one host; client, gateway and core contend for CPUs and
  memory. Client serialization, GC, sleep/wakeup lateness, blocked writes and
  queueing are included in intended-time latency. Profile the load generator
  separately before attributing a bottleneck to the engine.
- Open-loop throughput at an offered rate primarily confirms that load was
  delivered and drained. These finite bursts (including token-bucket burst
  capacity) are **not** a sustainable maximum-capacity measurement.
- Paired fill latency includes waiting for a counterparty order and differs
  from ACK latency. No market-data fan-out figure is claimed by this harness.
- Warmup is excluded, every ACK/final fill is required, rejections/timeouts fail,
  and sender backlog is not erased. HDR has 3 significant digits; extrema are
  quantized. Histograms are not synthetically corrected a second time.
- The new hardware, compiler and endpoints differ from historical reports;
  results cannot establish a historical regression or improvement.
- See [the full contract](docs/benchmark-methodology.md) for fields, authentication,
  symbol precision, failure policy and output schema. Nightly artifacts are for
  manual comparison; there is no automatic numerical pass/fail threshold.
