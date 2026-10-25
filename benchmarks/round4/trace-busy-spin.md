# Live FXP benchmark

Mode: **open**; 1 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 1000.1 | 997.4 | 1000.2 |
| max_send_lag_ns | 1858229.0 | 1054862.0 | 22650693.0 |
| ack_p50_ns | 10477567.0 | 10166271.0 | 10887167.0 |
| ack_p90_ns | 18776063.0 | 18366463.0 | 19513343.0 |
| ack_p99_ns | 30081023.0 | 20332543.0 | 40566783.0 |
| ack_p99_9_ns | 38830079.0 | 22691839.0 | 49479679.0 |
| ack_max_ns | 39845887.0 | 23674879.0 | 50462719.0 |
| fill_p50_ns | 10952703.0 | 10764287.0 | 11509759.0 |
| fill_p90_ns | 19267583.0 | 19120127.0 | 20168703.0 |
| fill_p99_ns | 31096831.0 | 21331967.0 | 41549823.0 |
| fill_p99_9_ns | 39845887.0 | 23674879.0 | 50462719.0 |
| fill_max_ns | 40828927.0 | 24674303.0 | 51445759.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
