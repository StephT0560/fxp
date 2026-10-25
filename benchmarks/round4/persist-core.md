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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 1537.7 | 1397.1 | 1584.2 |
| max_send_lag_ns | 58946.0 | 50265.0 | 67802.0 |
| ack_p50_ns | 574975.0 | 562687.0 | 591871.0 |
| ack_p90_ns | 850943.0 | 806399.0 | 954367.0 |
| ack_p99_ns | 2127871.0 | 1332223.0 | 2643967.0 |
| ack_p99_9_ns | 4296703.0 | 2555903.0 | 8691711.0 |
| ack_max_ns | 5681151.0 | 3545087.0 | 8912895.0 |
| fill_p50_ns | 945663.0 | 925183.0 | 964095.0 |
| fill_p90_ns | 1454079.0 | 1386495.0 | 1601535.0 |
| fill_p99_ns | 3098623.0 | 2215935.0 | 4874239.0 |
| fill_p99_9_ns | 6393855.0 | 3037183.0 | 9551871.0 |
| fill_max_ns | 7188479.0 | 4521983.0 | 9969663.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
