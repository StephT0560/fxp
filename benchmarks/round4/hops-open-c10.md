# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
- revision: 9a8d72546c7b3feb2e8a1367a829e6a1077b1bf2
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 9936.0 | 9932.2 | 9960.5 |
| max_send_lag_ns | 3070760.0 | 2774849.0 | 4748180.0 |
| ack_p50_ns | 1126399.0 | 1033215.0 | 1131519.0 |
| ack_p90_ns | 2373631.0 | 1987583.0 | 2541567.0 |
| ack_p99_ns | 5349375.0 | 4153343.0 | 15384575.0 |
| ack_p99_9_ns | 7340031.0 | 5402623.0 | 21102591.0 |
| ack_max_ns | 8335359.0 | 6651903.0 | 22708223.0 |
| fill_p50_ns | 1773567.0 | 1714175.0 | 1815551.0 |
| fill_p90_ns | 3018751.0 | 2760703.0 | 3176447.0 |
| fill_p99_ns | 5984255.0 | 4882431.0 | 15818751.0 |
| fill_p99_9_ns | 8138751.0 | 6037503.0 | 21200895.0 |
| fill_max_ns | 9863167.0 | 7659519.0 | 23117823.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
