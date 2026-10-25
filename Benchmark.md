# Corrected benchmark results and methodology

Measurements: 2026-10-25, source **`dff2cc33bb86b189ad4ba2cc30ad3fc86f61cca2`**.
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
| FXP encode, reused buffer | 163.6 [151.7, 166.7] |
| FXP encode, fresh allocation | 289.9 [289.5, 291.3] |
| FIX encode, reusable integer formatter | 127.9 [127.6, 128.2] |
| FIX encode, allocating format! baseline | 526.0 [525.2, 527.4] |
| SBE encode, stack | 29.3 [28.7, 31.3] |
| SBE encode, heap | 35.4 [35.2, 36.9] |
| FXP decode, owned schema | 382.5 [380.8, 386.4] |
| FIX decode, owned generic tag map | 1941.6 [1919.2, 1948.8] |
| SBE decode, owned full fixture | 114.2 [113.0, 120.6] |
| FXP measured encode+decode | 526.0 [524.5, 527.0] |
| FIX measured encode+generic-map decode | 2012.5 [2002.3, 2018.4] |
| SBE measured stack encode+owned decode | 120.1 [119.1, 121.4] |

**The reusable FIX encoder is faster than FXP encode in this run**
(127.9 versus 163.6 ns). The allocating FIX baseline is much slower,
so the old headline based only on format! overstated the advantage. SBE heap
(35.4 ns) is now slower than stack (29.3 ns); full owned SBE decode
is 114.2 ns, not the old ~1 ns scalar projection.
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
| open-c1 | 1,000 | 999.3 [999.1, 999.8] | 1,000 | 1,000 |
| open-c10 | 10,000 | 9974.4 [9960.1, 9976.6] | 10,000 | 10,000 |
| closed-c1 | 1,000 | 4266.0 [4129.6, 4654.8] | 1,000 | 1,000 |

All latency cells below show **median of the five per-run values [min, max]**,
in milliseconds. ACK means EXEC_NEW; fill means terminal ORDER_FILLED. Every
order contributes one sample to each endpoint; reports are not counted as orders.

| Scenario / endpoint | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms |
|---|---:|---:|---:|---:|---:|
| open-c1 / ack | 0.778 [0.757, 0.798] | 1.276 [1.248, 1.297] | 1.465 [1.408, 1.519] | 1.920 [1.582, 3.373] | 2.136 [1.612, 4.452] |
| open-c1 / fill | 1.320 [1.305, 1.360] | 2.169 [2.138, 2.212] | 2.406 [2.388, 2.484] | 2.695 [2.503, 4.522] | 2.929 [2.611, 5.382] |
| open-c10 / ack | 0.961 [0.901, 0.990] | 1.724 [1.595, 1.812] | 2.593 [1.907, 6.554] | 4.338 [2.669, 13.525] | 6.816 [3.398, 14.721] |
| open-c10 / fill | 1.646 [1.612, 1.669] | 2.542 [2.486, 2.646] | 3.371 [2.867, 6.853] | 4.649 [3.660, 14.262] | 6.824 [4.176, 14.746] |
| closed-c1 / ack | 0.206 [0.189, 0.218] | 0.283 [0.256, 0.297] | 0.392 [0.370, 0.419] | 1.217 [0.848, 2.241] | 1.403 [1.258, 3.965] |
| closed-c1 / fill | 0.338 [0.321, 0.354] | 0.534 [0.462, 0.569] | 0.744 [0.611, 0.761] | 1.455 [0.923, 2.615] | 1.775 [1.660, 4.338] |


For example, c10 open-loop ACK p99 ranges from
1.907 to 6.554 ms;
this spread is published, not discarded as an outlier. The maximum observed
run's terminal-fill tail is 14.746 ms.
A 1000-order run's p99.9 is an extreme with little statistical confidence.

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
