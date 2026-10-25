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
| orders_per_second | 24235.0 | 22924.6 | 24507.0 |
| max_send_lag_ns | 574030.0 | 356273.0 | 10302442.0 |
| ack_p50_ns | 1661951.0 | 1656831.0 | 1691647.0 |
| ack_p90_ns | 3108863.0 | 2965503.0 | 3379199.0 |
| ack_p99_ns | 10125311.0 | 9617407.0 | 11755519.0 |
| ack_p99_9_ns | 16523263.0 | 14548991.0 | 17907711.0 |
| ack_max_ns | 21725183.0 | 15851519.0 | 26099711.0 |
| fill_p50_ns | 2600959.0 | 2588671.0 | 2666495.0 |
| fill_p90_ns | 5058559.0 | 4829183.0 | 5443583.0 |
| fill_p99_ns | 13041663.0 | 12582911.0 | 14393343.0 |
| fill_p99_9_ns | 18563071.0 | 16547839.0 | 25034751.0 |
| fill_max_ns | 23773183.0 | 18087935.0 | 43843583.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
