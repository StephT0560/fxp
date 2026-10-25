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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 22150.8 | 20469.8 | 24470.2 |
| max_send_lag_ns | 569610.0 | 216858.0 | 12447253.0 |
| ack_p50_ns | 1778687.0 | 1629183.0 | 1887231.0 |
| ack_p90_ns | 3465215.0 | 3121151.0 | 3979263.0 |
| ack_p99_ns | 10878975.0 | 10158079.0 | 11821055.0 |
| ack_p99_9_ns | 19759103.0 | 15523839.0 | 19906559.0 |
| ack_max_ns | 24772607.0 | 18497535.0 | 29212671.0 |
| fill_p50_ns | 2820095.0 | 2543615.0 | 2951167.0 |
| fill_p90_ns | 5599231.0 | 5050367.0 | 6295551.0 |
| fill_p99_ns | 13991935.0 | 12443647.0 | 15474687.0 |
| fill_p99_9_ns | 24870911.0 | 19480575.0 | 26001407.0 |
| fill_max_ns | 33685503.0 | 22216703.0 | 44957695.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
