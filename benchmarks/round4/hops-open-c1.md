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
- revision: 9a8d72546c7b3feb2e8a1367a829e6a1077b1bf2
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 999.7 | 998.9 | 1000.3 |
| max_send_lag_ns | 2174456.0 | 1337859.0 | 2282964.0 |
| ack_p50_ns | 769023.0 | 757759.0 | 783871.0 |
| ack_p90_ns | 1238015.0 | 1223679.0 | 1275903.0 |
| ack_p99_ns | 2213887.0 | 2012159.0 | 3092479.0 |
| ack_p99_9_ns | 3831807.0 | 3043327.0 | 6041599.0 |
| ack_max_ns | 4759551.0 | 3332095.0 | 6078463.0 |
| fill_p50_ns | 1307647.0 | 1284095.0 | 1373183.0 |
| fill_p90_ns | 2177023.0 | 2142207.0 | 2234367.0 |
| fill_p99_ns | 3008511.0 | 2834431.0 | 3887103.0 |
| fill_p99_9_ns | 4247551.0 | 3839999.0 | 6090751.0 |
| fill_max_ns | 4837375.0 | 4362239.0 | 6107135.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
