# Live FXP benchmark

Mode: **closed**; 1 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 5983.1 | 4485.3 | 6047.4 |
| max_send_lag_ns | 66812.0 | 2048.0 | 189456.0 |
| ack_p50_ns | 140927.0 | 140543.0 | 144127.0 |
| ack_p90_ns | 223231.0 | 212991.0 | 281599.0 |
| ack_p99_ns | 457215.0 | 419327.0 | 1776639.0 |
| ack_p99_9_ns | 1528831.0 | 1095679.0 | 3741695.0 |
| ack_max_ns | 1675263.0 | 1543167.0 | 4464639.0 |
| fill_p50_ns | 244095.0 | 236799.0 | 256511.0 |
| fill_p90_ns | 371455.0 | 359679.0 | 456191.0 |
| fill_p99_ns | 756735.0 | 674815.0 | 2512895.0 |
| fill_p99_9_ns | 1780735.0 | 1683455.0 | 4481023.0 |
| fill_max_ns | 1974271.0 | 1850367.0 | 4784127.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
