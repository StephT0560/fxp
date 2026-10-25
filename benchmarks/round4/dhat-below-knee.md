# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 2000 orders/s/connection (ignored in closed mode).

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
- revision: 16c051471f09ac5c685be9c96ae2cb617a1b1495
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 5450.9 | 4837.4 | 5548.3 |
| max_send_lag_ns | 2711978.0 | 1979276.0 | 5740844.0 |
| ack_p50_ns | 674758655.0 | 649068543.0 | 710934527.0 |
| ack_p90_ns | 1200619519.0 | 1171259391.0 | 1390411775.0 |
| ack_p99_ns | 1321205759.0 | 1286602751.0 | 1528823807.0 |
| ack_p99_9_ns | 1332740095.0 | 1299185663.0 | 1562378239.0 |
| ack_max_ns | 1334837247.0 | 1303379967.0 | 1566572543.0 |
| fill_p50_ns | 674758655.0 | 651165695.0 | 712507391.0 |
| fill_p90_ns | 1202716671.0 | 1171259391.0 | 1391460351.0 |
| fill_p99_ns | 1321205759.0 | 1286602751.0 | 1533018111.0 |
| fill_p99_9_ns | 1332740095.0 | 1299185663.0 | 1562378239.0 |
| fill_max_ns | 1335885823.0 | 1303379967.0 | 1566572543.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
