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
| orders_per_second | 999.0 | 998.1 | 999.3 |
| max_send_lag_ns | 2056523.0 | 1916409.0 | 2649217.0 |
| ack_p50_ns | 768511.0 | 735231.0 | 777215.0 |
| ack_p90_ns | 1260543.0 | 1242111.0 | 1273855.0 |
| ack_p99_ns | 2463743.0 | 2332671.0 | 2910207.0 |
| ack_p99_9_ns | 3997695.0 | 3483647.0 | 4845567.0 |
| ack_max_ns | 4956159.0 | 3708927.0 | 5828607.0 |
| fill_p50_ns | 1314815.0 | 1306623.0 | 1329151.0 |
| fill_p90_ns | 2199551.0 | 2166783.0 | 2215935.0 |
| fill_p99_ns | 3192831.0 | 2918399.0 | 3237887.0 |
| fill_p99_9_ns | 4874239.0 | 4085759.0 | 5844991.0 |
| fill_max_ns | 5001215.0 | 4448255.0 | 6836223.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
