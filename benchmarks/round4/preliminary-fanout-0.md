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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 22827.0 | 20747.8 | 23764.0 |
| max_send_lag_ns | 117392.0 | 109555.0 | 453318.0 |
| ack_p50_ns | 351231.0 | 343039.0 | 356863.0 |
| ack_p90_ns | 684031.0 | 648191.0 | 701951.0 |
| ack_p99_ns | 1606655.0 | 1519615.0 | 2572287.0 |
| ack_p99_9_ns | 3936255.0 | 2920447.0 | 5423103.0 |
| ack_max_ns | 6402047.0 | 5222399.0 | 14114815.0 |
| fill_p50_ns | 555007.0 | 541183.0 | 563199.0 |
| fill_p90_ns | 1075199.0 | 1021951.0 | 1126399.0 |
| fill_p99_ns | 2498559.0 | 2232319.0 | 3788799.0 |
| fill_p99_9_ns | 6709247.0 | 5234687.0 | 14123007.0 |
| fill_max_ns | 7536639.0 | 6082559.0 | 14630911.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
