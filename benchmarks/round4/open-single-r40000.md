# Live FXP benchmark

Mode: **open**; 10 connections; 20000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 4000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 32757.1 | 26126.1 | 36487.7 |
| max_send_lag_ns | 96243967.0 | 62955750.0 | 321257299.0 |
| ack_p50_ns | 765984767.0 | 188874751.0 | 2634022911.0 |
| ack_p90_ns | 1496317951.0 | 701497343.0 | 2942304255.0 |
| ack_p99_ns | 1668284415.0 | 826802175.0 | 3019898879.0 |
| ack_p99_9_ns | 1704984575.0 | 859308031.0 | 3063939071.0 |
| ack_max_ns | 1731198975.0 | 872415231.0 | 3084910591.0 |
| fill_p50_ns | 766509055.0 | 189136895.0 | 2634022911.0 |
| fill_p90_ns | 1496317951.0 | 701497343.0 | 2942304255.0 |
| fill_p99_ns | 1668284415.0 | 826802175.0 | 3021996031.0 |
| fill_p99_9_ns | 1704984575.0 | 859832319.0 | 3063939071.0 |
| fill_max_ns | 1732247551.0 | 872939519.0 | 3084910591.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
