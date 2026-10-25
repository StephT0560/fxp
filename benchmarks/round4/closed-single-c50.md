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
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 24573.1 | 24346.7 | 25349.6 |
| max_send_lag_ns | 474079.0 | 199843.0 | 9627497.0 |
| ack_p50_ns | 1633279.0 | 1629183.0 | 1677311.0 |
| ack_p90_ns | 3020799.0 | 2922495.0 | 3100671.0 |
| ack_p99_ns | 9306111.0 | 8699903.0 | 9805823.0 |
| ack_p99_9_ns | 15269887.0 | 14098431.0 | 23560191.0 |
| ack_max_ns | 18939903.0 | 16703487.0 | 62849023.0 |
| fill_p50_ns | 2623487.0 | 2578431.0 | 2650111.0 |
| fill_p90_ns | 4820991.0 | 4653055.0 | 4931583.0 |
| fill_p99_ns | 11886591.0 | 11386879.0 | 12492799.0 |
| fill_p99_9_ns | 17989631.0 | 17121279.0 | 62128127.0 |
| fill_max_ns | 23445503.0 | 20037631.0 | 64815103.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
