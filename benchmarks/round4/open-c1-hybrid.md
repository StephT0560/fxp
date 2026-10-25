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
| orders_per_second | 999.3 | 999.2 | 999.9 |
| max_send_lag_ns | 2071558.0 | 1552938.0 | 2662699.0 |
| ack_p50_ns | 653823.0 | 652799.0 | 664063.0 |
| ack_p90_ns | 1112063.0 | 1094655.0 | 1129471.0 |
| ack_p99_ns | 1928191.0 | 1692671.0 | 2189311.0 |
| ack_p99_9_ns | 2603007.0 | 2478079.0 | 4558847.0 |
| ack_max_ns | 3131391.0 | 2709503.0 | 5541887.0 |
| fill_p50_ns | 1196031.0 | 1176575.0 | 1220607.0 |
| fill_p90_ns | 2041855.0 | 2024447.0 | 2073599.0 |
| fill_p99_ns | 2621439.0 | 2375679.0 | 3084287.0 |
| fill_p99_9_ns | 3575807.0 | 3176447.0 | 5550079.0 |
| fill_max_ns | 3715071.0 | 3596287.0 | 6549503.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
