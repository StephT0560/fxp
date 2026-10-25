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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 22607.4 | 21728.9 | 24805.1 |
| max_send_lag_ns | 846745.0 | 309242.0 | 4485601.0 |
| ack_p50_ns | 1708031.0 | 1638399.0 | 1778687.0 |
| ack_p90_ns | 3414015.0 | 2996223.0 | 3616767.0 |
| ack_p99_ns | 10444799.0 | 9248767.0 | 11763711.0 |
| ack_p99_9_ns | 16293887.0 | 15687679.0 | 18350079.0 |
| ack_max_ns | 22527999.0 | 17334271.0 | 27787263.0 |
| fill_p50_ns | 2693119.0 | 2557951.0 | 2811903.0 |
| fill_p90_ns | 5541887.0 | 4837375.0 | 5799935.0 |
| fill_p99_ns | 13819903.0 | 12746751.0 | 15409151.0 |
| fill_p99_9_ns | 23330815.0 | 17727487.0 | 24657919.0 |
| fill_max_ns | 28409855.0 | 20676607.0 | 37552127.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
