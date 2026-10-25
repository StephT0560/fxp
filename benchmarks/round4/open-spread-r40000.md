# Live FXP benchmark

Mode: **open**; 10 connections; 20000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 4000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 34801.3 | 32074.8 | 35913.7 |
| max_send_lag_ns | 71055839.0 | 61149555.0 | 194646212.0 |
| ack_p50_ns | 384303103.0 | 288620543.0 | 837287935.0 |
| ack_p90_ns | 1054867455.0 | 822083583.0 | 1606418431.0 |
| ack_p99_ns | 1134559231.0 | 954728447.0 | 1723858943.0 |
| ack_p99_9_ns | 1202716671.0 | 1005584383.0 | 1758461951.0 |
| ack_max_ns | 1232076799.0 | 1029177343.0 | 1776287743.0 |
| fill_p50_ns | 384565247.0 | 288620543.0 | 837287935.0 |
| fill_p90_ns | 1054867455.0 | 822607871.0 | 1607467007.0 |
| fill_p99_ns | 1134559231.0 | 955252735.0 | 1723858943.0 |
| fill_p99_9_ns | 1202716671.0 | 1005584383.0 | 1758461951.0 |
| fill_max_ns | 1232076799.0 | 1029177343.0 | 1777336319.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
