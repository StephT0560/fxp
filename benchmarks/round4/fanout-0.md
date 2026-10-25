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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 22371.9 | 21877.1 | 23848.6 |
| max_send_lag_ns | 147429.0 | 134203.0 | 1212598.0 |
| ack_p50_ns | 355327.0 | 346367.0 | 366847.0 |
| ack_p90_ns | 693247.0 | 642047.0 | 700415.0 |
| ack_p99_ns | 1853439.0 | 1628159.0 | 2056191.0 |
| ack_p99_9_ns | 3684351.0 | 3342335.0 | 5554175.0 |
| ack_max_ns | 6688767.0 | 3842047.0 | 13058047.0 |
| fill_p50_ns | 559615.0 | 547327.0 | 571391.0 |
| fill_p90_ns | 1090559.0 | 1023999.0 | 1120255.0 |
| fill_p99_ns | 2582527.0 | 2193407.0 | 3125247.0 |
| fill_p99_9_ns | 6340607.0 | 3964927.0 | 13066239.0 |
| fill_max_ns | 7356415.0 | 4849663.0 | 13770751.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
