# Live FXP benchmark

Mode: **closed**; 1 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 5427.8 | 3914.2 | 5586.9 |
| max_send_lag_ns | 49448.0 | 22129.0 | 114886.0 |
| ack_p50_ns | 143103.0 | 141951.0 | 179839.0 |
| ack_p90_ns | 238719.0 | 232063.0 | 345855.0 |
| ack_p99_ns | 566271.0 | 501759.0 | 1214463.0 |
| ack_p99_9_ns | 1931263.0 | 1372159.0 | 3536895.0 |
| ack_max_ns | 3553279.0 | 1713151.0 | 11182079.0 |
| fill_p50_ns | 272127.0 | 267007.0 | 293119.0 |
| fill_p90_ns | 421631.0 | 388095.0 | 596991.0 |
| fill_p99_ns | 858623.0 | 772095.0 | 2109439.0 |
| fill_p99_9_ns | 3577855.0 | 1575935.0 | 11214847.0 |
| fill_max_ns | 4102143.0 | 1905663.0 | 13942783.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
