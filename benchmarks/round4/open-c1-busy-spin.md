# Live FXP benchmark

Mode: **open**; 1 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

Transport: TLS=false; client certificate=false.

- arch: amd64
- client_cpu_affinity: 2
- cpu: INTEL(R) XEON(R) PLATINUM 8573C
- go: go1.26.10
- gomaxprocs: 1
- launch_env_FXP_GW_RATE: 1000000
- launch_env_FXP_GW_RATE_BURST: 1000000
- launch_env_FXP_SYMBOL_CONFIG: 
- launch_env_RUST_LOG: error
- launch_env_TOKIO_WORKER_THREADS: 1
- logical_cpus: 1
- os: linux
- pinning_notes: core=0 gateway=1 generator=2; GOMAXPROCS=1 TOKIO_WORKER_THREADS=1; shared cgroup quota 2 CPUs
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 1000.2 | 1000.0 | 1000.3 |
| max_send_lag_ns | 1993904.0 | 483743.0 | 3357297.0 |
| ack_p50_ns | 10051583.0 | 9838591.0 | 10248191.0 |
| ack_p90_ns | 18300927.0 | 18251775.0 | 18563071.0 |
| ack_p99_ns | 20283391.0 | 20086783.0 | 21446655.0 |
| ack_p99_9_ns | 23248895.0 | 21626879.0 | 23445503.0 |
| ack_max_ns | 23609343.0 | 22626303.0 | 24231935.0 |
| fill_p50_ns | 10665983.0 | 10305535.0 | 10903551.0 |
| fill_p90_ns | 18939903.0 | 18677759.0 | 19251199.0 |
| fill_p99_ns | 21102591.0 | 20922367.0 | 21987327.0 |
| fill_p99_9_ns | 23248895.0 | 21774335.0 | 24444927.0 |
| fill_max_ns | 24248319.0 | 22642687.0 | 24870911.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
