# Live FXP benchmark

Mode: **open**; 10 connections; 2500 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 500 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 4994.4 | 4992.2 | 4996.1 |
| max_send_lag_ns | 7317825.0 | 5232138.0 | 9742334.0 |
| ack_p50_ns | 979455.0 | 957951.0 | 1032703.0 |
| ack_p90_ns | 1524735.0 | 1432575.0 | 1721343.0 |
| ack_p99_ns | 3735551.0 | 2615295.0 | 18464767.0 |
| ack_p99_9_ns | 15998975.0 | 6598655.0 | 31440895.0 |
| ack_max_ns | 20430847.0 | 9535487.0 | 35717119.0 |
| fill_p50_ns | 2445311.0 | 2418687.0 | 2506751.0 |
| fill_p90_ns | 3454975.0 | 3356671.0 | 3557375.0 |
| fill_p99_ns | 5337087.0 | 4419583.0 | 19120127.0 |
| fill_p99_9_ns | 16293887.0 | 9142271.0 | 31735807.0 |
| fill_max_ns | 23724031.0 | 11534335.0 | 38273023.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
