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
| orders_per_second | 23842.6 | 21542.5 | 24740.4 |
| max_send_lag_ns | 367447.0 | 202118.0 | 2164896.0 |
| ack_p50_ns | 1713151.0 | 1641471.0 | 1730559.0 |
| ack_p90_ns | 3117055.0 | 3047423.0 | 3837951.0 |
| ack_p99_ns | 11034623.0 | 8749055.0 | 11288575.0 |
| ack_p99_9_ns | 15499263.0 | 13557759.0 | 21020671.0 |
| ack_max_ns | 18939903.0 | 15269887.0 | 25198591.0 |
| fill_p50_ns | 2674687.0 | 2580479.0 | 2721791.0 |
| fill_p90_ns | 5001215.0 | 4870143.0 | 6127615.0 |
| fill_p99_ns | 13557759.0 | 11411455.0 | 15466495.0 |
| fill_p99_9_ns | 18104319.0 | 15122431.0 | 26099711.0 |
| fill_max_ns | 28278783.0 | 20905983.0 | 34930687.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
