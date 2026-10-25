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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 9932.8 | 9642.7 | 9939.7 |
| max_send_lag_ns | 6561482.0 | 3066562.0 | 139008114.0 |
| ack_p50_ns | 1396735.0 | 1059839.0 | 43712511.0 |
| ack_p90_ns | 4984831.0 | 1836031.0 | 165150719.0 |
| ack_p99_ns | 13983743.0 | 4341759.0 | 263323647.0 |
| ack_p99_9_ns | 18382847.0 | 8757247.0 | 315359231.0 |
| ack_max_ns | 19628031.0 | 10166271.0 | 325058559.0 |
| fill_p50_ns | 2154495.0 | 1743871.0 | 44105727.0 |
| fill_p90_ns | 5599231.0 | 2693119.0 | 165412863.0 |
| fill_p99_ns | 14688255.0 | 4853759.0 | 263454719.0 |
| fill_p99_9_ns | 18972671.0 | 8765439.0 | 315359231.0 |
| fill_max_ns | 21938175.0 | 10199039.0 | 325320703.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
