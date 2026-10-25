# Round-4 raw evidence

Read [the report](../../docs/performance/profile-r4.md) before interpreting these
files. This is one shared container, not the round-4 report's original VM.
CPU placement is core=0, gateway=1, generator=2; the bridge shares CPU 1. The
cgroup has a two-CPU quota. No executables or perf.data files are committed.

- `open-c1-{sleep,busy-spin,hybrid}`, `open-c10`, `closed-c1`: primary pacing runs.
- `open-{single,spread}-r*`: aggregate offered rates with ten sessions and
  five-second intended windows. Failures have an error and no success summary.
  `closed-{single,spread}-c*` ramps concurrency. Spread pairs sessions, so ten
  sessions use five symbols, not ten.
- `hops-*`: buffered CLOCK_MONOTONIC events and joined per-hop statistics.
- `fix-{memory,strict,group,async}-corelog-*`: real FIX path, dictionary enabled;
  persistence modes compared with the same Python fixture, not FXP latency.
- `fanout-*`: active subscribers, post-warmup snapshot barrier, same Go fixture.
- `trace-{sleep,busy-spin}-*`: raw 1k/s Go runtime traces and scheduling profiles.
- `perf-*`, `dhat-*`, `profile-*`: instrumented runs; their throughput is not a
  capacity baseline. Perf is user CPU-clock sampling at 99 Hz. Go block/mutex
  sampling is full rate. Dhat uses a separate allocator/build and shorter loads.
- `engine-criterion-*`, `owner-benchmarks.txt`: CPU/allocation microbenchmarks.
- `before-*`, `after-*`, `framing-*`: five-run A/B evidence for the reverted frame
  construction experiment. The predeclared target was c50 completed throughput.
- `preliminary-*`: excluded from primary claims. The short offered-rate ramp
  used only 1,000 orders/session. Initial fan-out subscribed before warmup and
  silently missed the first symbol subscription. The first perf c50 capture
  still had the framing trial binary loaded; build IDs identified it and the
  restored-binary capture was repeated. Initial hop captures overlapped early
  build/test activity and were also repeated serially. These are retained for
  transparency.

`*.json.gz` preserves full JSON/HDR data losslessly. Go `*.pprof` files are raw
profile data, not executable binaries. Requested 1k/s runtime traces are retained;
large incidental below-knee runtime traces are reduced to scheduling profiles.
The workload JSON, CPU/alloc/block/mutex profiles, Rust folded stacks, SVGs and
raw perf-script stacks remain. `*-execution.json` records command, return code,
CPU consumption and cgroup counters (later runs also include binary hashes).
Per-run percentiles, not pooled percentiles, underpin summary ranges.

`perf sched` is unavailable: scheduler tracepoints are not mounted. See
`perf-sched.txt`. `perf-unavailable.txt` records the initial package-install
failure and successful syscall probe; `perf-probe.txt` records the working local
Debian perf. These failures do not mean CPU sampling was unavailable.

`make_capacity_plot.py` renders the two FXP ramp panels using Matplotlib 3.10.8;
failed loads are annotated without inventing a throughput or p99 data point.
