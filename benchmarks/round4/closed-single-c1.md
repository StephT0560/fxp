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
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 5704.5 | 5547.3 | 6151.2 |
| max_send_lag_ns | 30903.0 | 1946.0 | 42581.0 |
| ack_p50_ns | 142335.0 | 140543.0 | 142847.0 |
| ack_p90_ns | 226431.0 | 208127.0 | 260735.0 |
| ack_p99_ns | 545791.0 | 346879.0 | 574463.0 |
| ack_p99_9_ns | 1483775.0 | 429311.0 | 1768447.0 |
| ack_max_ns | 1683455.0 | 1567743.0 | 3639295.0 |
| fill_p50_ns | 246271.0 | 243583.0 | 258303.0 |
| fill_p90_ns | 385279.0 | 358911.0 | 420351.0 |
| fill_p99_ns | 760319.0 | 609279.0 | 1101823.0 |
| fill_p99_9_ns | 1908735.0 | 1132543.0 | 2256895.0 |
| fill_max_ns | 2020351.0 | 1826815.0 | 6082559.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
