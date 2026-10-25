# Live FXP benchmark

Mode: **closed**; 50 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
- revision: 16c051471f09ac5c685be9c96ae2cb617a1b1495
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 22941.9 | 18885.6 | 23915.4 |
| max_send_lag_ns | 1401092.0 | 827577.0 | 9646974.0 |
| ack_p50_ns | 1743871.0 | 1700863.0 | 1859583.0 |
| ack_p90_ns | 3225599.0 | 3096575.0 | 4509695.0 |
| ack_p99_ns | 11116543.0 | 8962047.0 | 14073855.0 |
| ack_p99_9_ns | 15171583.0 | 14655487.0 | 37486591.0 |
| ack_max_ns | 22167551.0 | 15892479.0 | 42696703.0 |
| fill_p50_ns | 2717695.0 | 2664447.0 | 2996223.0 |
| fill_p90_ns | 5275647.0 | 5005311.0 | 7053311.0 |
| fill_p99_ns | 13148159.0 | 12451839.0 | 19841023.0 |
| fill_p99_9_ns | 19349503.0 | 16654335.0 | 43319295.0 |
| fill_max_ns | 24084479.0 | 17711103.0 | 52625407.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
