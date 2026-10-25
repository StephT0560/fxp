# Live FXP benchmark

Mode: **closed**; 100 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 24091.4 | 20397.6 | 24141.4 |
| max_send_lag_ns | 1359643.0 | 864588.0 | 1611329.0 |
| ack_p50_ns | 3452927.0 | 3407871.0 | 3624959.0 |
| ack_p90_ns | 6397951.0 | 6377471.0 | 7249919.0 |
| ack_p99_ns | 16572415.0 | 15630335.0 | 22069247.0 |
| ack_p99_9_ns | 27394047.0 | 22003711.0 | 119996415.0 |
| ack_max_ns | 42598399.0 | 40042495.0 | 200671231.0 |
| fill_p50_ns | 5296127.0 | 5263359.0 | 5615615.0 |
| fill_p90_ns | 10395647.0 | 10289151.0 | 11747327.0 |
| fill_p99_ns | 20971519.0 | 20430847.0 | 29999103.0 |
| fill_p99_9_ns | 37027839.0 | 34308095.0 | 181403647.0 |
| fill_max_ns | 51478527.0 | 46956543.0 | 291241983.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
