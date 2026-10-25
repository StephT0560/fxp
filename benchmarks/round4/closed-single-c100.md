# Live FXP benchmark

Mode: **closed**; 100 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 24187.1 | 21616.6 | 24934.0 |
| max_send_lag_ns | 1123156.0 | 336392.0 | 3626907.0 |
| ack_p50_ns | 3457023.0 | 3393535.0 | 3526655.0 |
| ack_p90_ns | 6381567.0 | 5976063.0 | 6541311.0 |
| ack_p99_ns | 14409727.0 | 14155775.0 | 15392767.0 |
| ack_p99_9_ns | 28049407.0 | 22822911.0 | 32342015.0 |
| ack_max_ns | 37650431.0 | 37453823.0 | 457703423.0 |
| fill_p50_ns | 5365759.0 | 5267455.0 | 5480447.0 |
| fill_p90_ns | 10108927.0 | 9666559.0 | 10264575.0 |
| fill_p99_ns | 19333119.0 | 18710527.0 | 23101439.0 |
| fill_p99_9_ns | 33505279.0 | 27607039.0 | 456130559.0 |
| fill_max_ns | 44695551.0 | 38535167.0 | 461111295.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
