# Live FXP benchmark

Mode: **closed**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 8817.1 | 8268.3 | 8992.3 |
| max_send_lag_ns | 340348.0 | 132625.0 | 1176700.0 |
| ack_p50_ns | 1017343.0 | 1010175.0 | 1038335.0 |
| ack_p90_ns | 1675263.0 | 1650687.0 | 1762303.0 |
| ack_p99_ns | 3158015.0 | 3063807.0 | 4612095.0 |
| ack_p99_9_ns | 5644287.0 | 5095423.0 | 8396799.0 |
| ack_max_ns | 7458815.0 | 6643711.0 | 17252351.0 |
| fill_p50_ns | 1519615.0 | 1509375.0 | 1572863.0 |
| fill_p90_ns | 2689023.0 | 2643967.0 | 2850815.0 |
| fill_p99_ns | 4792319.0 | 4358143.0 | 7094271.0 |
| fill_p99_9_ns | 7417855.0 | 6647807.0 | 15384575.0 |
| fill_max_ns | 8577023.0 | 7720959.0 | 21151743.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
