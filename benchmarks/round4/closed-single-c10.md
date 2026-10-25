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
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 23360.2 | 20381.0 | 24843.8 |
| max_send_lag_ns | 90605.0 | 54261.0 | 386043.0 |
| ack_p50_ns | 344575.0 | 335359.0 | 359679.0 |
| ack_p90_ns | 653823.0 | 613375.0 | 757759.0 |
| ack_p99_ns | 1828863.0 | 1439743.0 | 2410495.0 |
| ack_p99_9_ns | 4612095.0 | 2332671.0 | 6582271.0 |
| ack_max_ns | 8855551.0 | 4997119.0 | 15966207.0 |
| fill_p50_ns | 547327.0 | 529919.0 | 571903.0 |
| fill_p90_ns | 1023999.0 | 969215.0 | 1210367.0 |
| fill_p99_ns | 2326527.0 | 2061311.0 | 3856383.0 |
| fill_p99_9_ns | 8798207.0 | 4861951.0 | 15966207.0 |
| fill_max_ns | 9814015.0 | 5320703.0 | 16261119.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
