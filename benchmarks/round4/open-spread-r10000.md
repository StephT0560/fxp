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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 9984.1 | 9971.9 | 9986.8 |
| max_send_lag_ns | 20143547.0 | 13550163.0 | 97101838.0 |
| ack_p50_ns | 975359.0 | 958463.0 | 1003007.0 |
| ack_p90_ns | 1865727.0 | 1753087.0 | 2766847.0 |
| ack_p99_ns | 22118399.0 | 6430719.0 | 69009407.0 |
| ack_p99_9_ns | 52330495.0 | 19316735.0 | 110231551.0 |
| ack_max_ns | 58556415.0 | 26394623.0 | 119144447.0 |
| fill_p50_ns | 1618943.0 | 1555455.0 | 1667071.0 |
| fill_p90_ns | 2674687.0 | 2578431.0 | 3393535.0 |
| fill_p99_ns | 22872063.0 | 7184383.0 | 69664767.0 |
| fill_p99_9_ns | 52625407.0 | 20316159.0 | 110886911.0 |
| fill_max_ns | 58589183.0 | 27394047.0 | 120193023.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
