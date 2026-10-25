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
- revision: c5210ec11d372721043c4b75ae7606d71beb9e44
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 24061.1 | 20279.4 | 24972.8 |
| max_send_lag_ns | 544552.0 | 237804.0 | 9639080.0 |
| ack_p50_ns | 1677311.0 | 1636351.0 | 1821695.0 |
| ack_p90_ns | 3131391.0 | 2945023.0 | 4179967.0 |
| ack_p99_ns | 11206655.0 | 8470527.0 | 14524415.0 |
| ack_p99_9_ns | 17055743.0 | 15695871.0 | 29081599.0 |
| ack_max_ns | 21086207.0 | 19365887.0 | 38174719.0 |
| fill_p50_ns | 2611199.0 | 2547711.0 | 2895871.0 |
| fill_p90_ns | 5013503.0 | 4759551.0 | 6529023.0 |
| fill_p99_ns | 14286847.0 | 11165695.0 | 25427967.0 |
| fill_p99_9_ns | 19873791.0 | 19005439.0 | 33914879.0 |
| fill_max_ns | 23527423.0 | 22822911.0 | 43909119.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
