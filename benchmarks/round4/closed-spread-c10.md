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
| orders_per_second | 23640.9 | 18720.9 | 24987.0 |
| max_send_lag_ns | 229579.0 | 115151.0 | 1381697.0 |
| ack_p50_ns | 340735.0 | 332287.0 | 364031.0 |
| ack_p90_ns | 647679.0 | 608767.0 | 781311.0 |
| ack_p99_ns | 1861631.0 | 1483775.0 | 2709503.0 |
| ack_p99_9_ns | 3823615.0 | 2406399.0 | 17039359.0 |
| ack_max_ns | 6905855.0 | 2842623.0 | 38699007.0 |
| fill_p50_ns | 530943.0 | 518143.0 | 572927.0 |
| fill_p90_ns | 1036799.0 | 959999.0 | 1257471.0 |
| fill_p99_ns | 2424831.0 | 2016255.0 | 3592191.0 |
| fill_p99_9_ns | 4833279.0 | 3260415.0 | 23527423.0 |
| fill_max_ns | 9027583.0 | 3663871.0 | 40894463.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
