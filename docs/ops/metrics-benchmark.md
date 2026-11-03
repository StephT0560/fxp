# Metrics before/after evidence

Measured November 3, 2026 on one shared Linux VM. Baseline main is
`314bdd5fa7222e61368ebecf3c2c3db50f50a0a8`; instrumented code is
`09bfe856f211dd3742335a3a14efee618bf116bf`. The later review fix adds a 32-connection admission cap to the gateway metrics
listener; the trading-message instrumentation measured here is unchanged.
The table records the original instrumentation benchmark, before that listener
admission fix. The branch changes no engine
or order-book logic.

The existing `scripts/bench.sh` harness ran all three states sequentially:
baseline, changed with metrics off, changed with both endpoints enabled and
scraped once per second. Every live scenario has five independent repetitions,
100 completed warmup orders per connection, then 1000 measured orders per
connection. Open loop is the existing absolute-deadline `sleep` pacer at 1000
orders/second/connection. `open-c10` spreads paired connections across symbols;
other cases use one symbol. Closed loop waits for each ACK. STP is NONE, GTC
limit quantity 1, price 300000 minor units at scale 2. All orders complete ACK
and terminal fill, with no rejection/dropped response. No external orders, MD
subscribers, session schedule, persistence, TLS or diagnostic trace is enabled.

Environment: AMD EPYC 9V74, Linux x86_64 kernel 6.18.57, five visible CPUs
(affinity 0–4), cgroup CPU quota 4 CPUs, no explicit pinning, shared host. Rust
1.99.1, Go 1.26.10, GOMAXPROCS=4, TOKIO_WORKER_THREADS=2, gateway rate/burst
2000/4000. Inherited RUST_LOG was
`codex_exec_server=info,codex_cli=info,error` for every state. Compiler evidence
is local. Finite tests over loopback are not production capacity or an SLO.

## Live result

Cells are **median [min–max] of the five per-run statistics**, not pooled
percentiles. ACK is EXEC_NEW measured from the intended send deadline in open
mode; it includes generator lag. Latency units here are milliseconds; the
[45-row CSV](metrics-benchmark.csv) preserves per-run ns values, counts,
after-send ACK statistics and fill p99. HDR values have three significant digits.

| Workload | State | Completed orders/s | ACK p50 ms | ACK p99 ms |
| --- | --- | --- | --- | --- |
| open-c1 | before | 999.454 [995.845–999.869] | 0.795 [0.769–0.854] | 2.976 [1.790–10.134] |
| open-c1 | after-disabled | 999.504 [998.876–1000.269] | 0.744 [0.719–0.779] | 1.826 [1.453–4.053] |
| open-c1 | after-enabled | 999.540 [999.229–1000.050] | 0.806 [0.764–0.836] | 3.236 [2.335–16.761] |
| open-c10 | before | 9929.358 [9927.966–9941.348] | 1.020 [0.949–1.152] | 5.431 [3.744–47.186] |
| open-c10 | after-disabled | 9946.214 [9937.113–9956.812] | 0.925 [0.902–1.006] | 3.420 [3.072–6.062] |
| open-c10 | after-enabled | 9940.400 [9928.743–9946.068] | 0.922 [0.902–1.055] | 4.751 [2.200–8.143] |
| closed-c1 | before | 3318.753 [2276.948–3806.284] | 0.214 [0.198–0.242] | 1.221 [0.794–3.209] |
| closed-c1 | after-disabled | 5038.630 [4489.687–5652.202] | 0.165 [0.146–0.168] | 0.487 [0.447–0.604] |
| closed-c1 | after-enabled | 5425.339 [5098.201–5967.052] | 0.157 [0.140–0.161] | 0.417 [0.316–0.504] |

No meaningful throughput loss or consistent ACK-latency regression was observed
in these finite workloads. Enabled open-c1 median ACK p50 is 1.4% slower and p99
8.7% slower than baseline; its worst per-run p99 is also worse (16.761 vs
10.134 ms), so the slower tail is reported rather than discarded. Their run
spreads overlap. Open-c10 median latency is lower; open-loop throughput differs
by less than 0.2%. Closed-loop throughput is higher in both changed states, but
this sequential shared-host experiment cannot attribute an improvement to the
code. It cannot establish a sub-percent regression bound. There is no invented
pass/fail timing threshold: interpret with the existing
[benchmark contract](../benchmark-methodology.md).

The journal latency histogram is validated by the recorded-stack integration
test, not this persistence-disabled throughput comparison. These measurements
do not claim recording/fsync throughput or large-fanout capacity.

## Encoding fixture cross-check

The full existing Criterion suite completed in all three states. Selected
median point estimates, in ns (these exclude metric collection, networking and
matching):

| Fixture | Baseline | Changed, disabled | Changed, enabled |
| --- | --- | --- | --- |
| new order FXP preallocated encode | 116.127 | 112.198 | 113.198 |
| new order FXP protobuf decode | 326.946 | 326.129 | 309.642 |

These implementation-fixture numbers do not measure metrics overhead; the live
comparison above does. The encoding code was not changed.

## Reproduce

After installing normal build prerequisites, with ports 8080–8082 free:

```bash
# At the baseline revision (preserve your working branch/changes):
bash scripts/bench.sh target/ops/before
# At the instrumented revision:
unset FXP_CORE_METRICS_ADDR FXP_GW_METRICS_ADDR
bash scripts/bench.sh target/ops/after-disabled
FXP_CORE_METRICS_ADDR=9190 FXP_GW_METRICS_ADDR=9191 \
  bash scripts/bench.sh target/ops/after-enabled
```

For the enabled case, a separate loopback scraper requests both `/metrics`
endpoints once per second while the harness runs, catching connection-refused
between startup/shutdown. Run no other tests/benchmarks concurrently. The harness
also runs Criterion encoding fixtures after stopping live processes, with 50
samples, 1 s warmup and 2 s measurement per fixture. They exclude matching,
networking and metric collection, as the existing methodology specifies.

Original JSON/HDR files, Markdown output, encoding estimates and environment
records remain under ignored `target/ops/{before,after-disabled,after-enabled}`
in the execution workspace. The small committed CSV is the reviewable per-run
projection, not a substitute claim about pooled HDR distributions. Original JSON
hashes:

| Original artifact, relative to target/ops | SHA-256 |
| --- | --- |
| `before/open-c1.json` | `606b646461a7aa162434aaf6c6a7fb5f7e90a801cb358b1d2803b601b26b6bfa` |
| `after-disabled/open-c1.json` | `da5a74f22412ca293a8e923e815c4d05492fe27cd6875d1227ba6a63526d9136` |
| `after-enabled/open-c1.json` | `375c7d939827f5d679dc45ae5a5178517c6911ab7face2878949c6ee80ff3d31` |
| `before/open-c10.json` | `42933fc0bc8d9cb96af39ba709032d50fdc39139a81a973eb0c14561efeb5036` |
| `after-disabled/open-c10.json` | `6ba7be40cdcf0d0b51c7571c464abc645a05f5d4668a5758bfe6d9a8f1a0af92` |
| `after-enabled/open-c10.json` | `8b507c27c40a5df7c59e12d782533c211e37e7956ff7ab5c6748d6f68c27db38` |
| `before/closed-c1.json` | `ec19209d54669890226f826d350da4acf4b4109728ef86d8f4a362e9cfba49e0` |
| `after-disabled/closed-c1.json` | `911317cf3a27dd7179f72b6b26dca322543456f98c07f759a4b9e8c2bcb1b2a7` |
| `after-enabled/closed-c1.json` | `e9cfc2f5b82015f3cb70c6f5d974face73ff11359c257ff374a734c395c45521` |
