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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 999.9 | 999.4 | 1000.1 |
| max_send_lag_ns | 1938702.0 | 1467788.0 | 2109746.0 |
| ack_p50_ns | 796159.0 | 750591.0 | 816127.0 |
| ack_p90_ns | 1352703.0 | 1308671.0 | 1497087.0 |
| ack_p99_ns | 2512895.0 | 2506751.0 | 3031039.0 |
| ack_p99_9_ns | 4012031.0 | 3416063.0 | 5222399.0 |
| ack_max_ns | 4915199.0 | 3508223.0 | 6008831.0 |
| fill_p50_ns | 1354751.0 | 1308671.0 | 1444863.0 |
| fill_p90_ns | 2217983.0 | 2181119.0 | 2318335.0 |
| fill_p99_ns | 3444735.0 | 3407871.0 | 3792895.0 |
| fill_p99_9_ns | 5021695.0 | 4268031.0 | 6017023.0 |
| fill_max_ns | 5922815.0 | 4915199.0 | 7012351.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
