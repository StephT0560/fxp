# Live FXP benchmark

Mode: **open**; 10 connections; 5000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 9987.2 | 9985.0 | 9988.7 |
| max_send_lag_ns | 17491097.0 | 13950268.0 | 19901898.0 |
| ack_p50_ns | 954879.0 | 952319.0 | 962559.0 |
| ack_p90_ns | 1778687.0 | 1751039.0 | 1803263.0 |
| ack_p99_ns | 11436031.0 | 4587519.0 | 19021823.0 |
| ack_p99_9_ns | 24903679.0 | 15974399.0 | 57212927.0 |
| ack_max_ns | 32669695.0 | 22167551.0 | 61865983.0 |
| fill_p50_ns | 1593343.0 | 1570815.0 | 1626111.0 |
| fill_p90_ns | 2603007.0 | 2570239.0 | 2652159.0 |
| fill_p99_ns | 12337151.0 | 5173247.0 | 19447807.0 |
| fill_p99_9_ns | 25411583.0 | 16130047.0 | 57769983.0 |
| fill_max_ns | 32686079.0 | 22315007.0 | 62881791.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
