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
| orders_per_second | 1460.5 | 1166.0 | 22026.8 |
| max_send_lag_ns | 426111.0 | 64631.0 | 966825.0 |
| ack_p50_ns | 6479871.0 | 365823.0 | 7372799.0 |
| ack_p90_ns | 11034623.0 | 656383.0 | 15835135.0 |
| ack_p99_ns | 16154623.0 | 2097151.0 | 23347199.0 |
| ack_p99_9_ns | 24854527.0 | 3854335.0 | 40730623.0 |
| ack_max_ns | 28770303.0 | 4444159.0 | 47513599.0 |
| fill_p50_ns | 9584639.0 | 569343.0 | 11673599.0 |
| fill_p90_ns | 16826367.0 | 1071103.0 | 22839295.0 |
| fill_p99_ns | 24281087.0 | 3065855.0 | 32620543.0 |
| fill_p99_9_ns | 32931839.0 | 4444159.0 | 51412991.0 |
| fill_max_ns | 45907967.0 | 5210111.0 | 54099967.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
