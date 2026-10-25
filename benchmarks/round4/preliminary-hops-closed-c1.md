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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 5585.9 | 5028.6 | 5655.7 |
| max_send_lag_ns | 23759.0 | 1423.0 | 61282.0 |
| ack_p50_ns | 141951.0 | 140799.0 | 144383.0 |
| ack_p90_ns | 232447.0 | 226815.0 | 255231.0 |
| ack_p99_ns | 567807.0 | 526335.0 | 965631.0 |
| ack_p99_9_ns | 1511423.0 | 1058815.0 | 2652159.0 |
| ack_max_ns | 1779711.0 | 1205247.0 | 4706303.0 |
| fill_p50_ns | 270847.0 | 266239.0 | 273151.0 |
| fill_p90_ns | 400895.0 | 392447.0 | 446719.0 |
| fill_p99_ns | 866815.0 | 773631.0 | 1481727.0 |
| fill_p99_9_ns | 2084863.0 | 1802239.0 | 4395007.0 |
| fill_max_ns | 2592767.0 | 2115583.0 | 5005311.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
