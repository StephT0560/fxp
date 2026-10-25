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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 5862.3 | 5691.8 | 6077.4 |
| max_send_lag_ns | 60914.0 | 28642.0 | 201266.0 |
| ack_p50_ns | 140799.0 | 139775.0 | 142847.0 |
| ack_p90_ns | 214015.0 | 208767.0 | 231295.0 |
| ack_p99_ns | 552447.0 | 451327.0 | 587263.0 |
| ack_p99_9_ns | 1534975.0 | 1294335.0 | 1580031.0 |
| ack_max_ns | 1677311.0 | 1580031.0 | 3930111.0 |
| fill_p50_ns | 242815.0 | 237439.0 | 250751.0 |
| fill_p90_ns | 370431.0 | 358143.0 | 406015.0 |
| fill_p99_ns | 881663.0 | 747519.0 | 893951.0 |
| fill_p99_9_ns | 1747967.0 | 1603583.0 | 3946495.0 |
| fill_max_ns | 1882111.0 | 1737727.0 | 4448255.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
