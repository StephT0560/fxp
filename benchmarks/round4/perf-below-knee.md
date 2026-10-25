# Live FXP benchmark

Mode: **open**; 10 connections; 10000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 2000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 19968.3 | 19944.8 | 19971.1 |
| max_send_lag_ns | 14034880.0 | 9263181.0 | 68981731.0 |
| ack_p50_ns | 1386495.0 | 1282047.0 | 4382719.0 |
| ack_p90_ns | 6385663.0 | 3842047.0 | 89194495.0 |
| ack_p99_ns | 46071807.0 | 9969663.0 | 148766719.0 |
| ack_p99_9_ns | 84279295.0 | 15572991.0 | 170655743.0 |
| ack_max_ns | 94699519.0 | 21692415.0 | 179830783.0 |
| fill_p50_ns | 1725439.0 | 1622015.0 | 4669439.0 |
| fill_p90_ns | 6647807.0 | 4130815.0 | 89391103.0 |
| fill_p99_ns | 46137343.0 | 10330111.0 | 148897791.0 |
| fill_p99_9_ns | 84410367.0 | 15712255.0 | 170655743.0 |
| fill_max_ns | 94699519.0 | 21790719.0 | 179830783.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
