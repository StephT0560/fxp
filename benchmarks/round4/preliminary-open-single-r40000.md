# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 4000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 38019.9 | 37273.0 | 38558.2 |
| max_send_lag_ns | 5457200.0 | 2416749.0 | 10971006.0 |
| ack_p50_ns | 8413183.0 | 6610943.0 | 13836287.0 |
| ack_p90_ns | 13623295.0 | 11837439.0 | 30097407.0 |
| ack_p99_ns | 17661951.0 | 16556031.0 | 39976959.0 |
| ack_p99_9_ns | 21282815.0 | 18841599.0 | 45973503.0 |
| ack_max_ns | 22315007.0 | 20430847.0 | 48398335.0 |
| fill_p50_ns | 8560639.0 | 6762495.0 | 13967359.0 |
| fill_p90_ns | 13762559.0 | 11927551.0 | 30228479.0 |
| fill_p99_ns | 17711103.0 | 16564223.0 | 40108031.0 |
| fill_p99_9_ns | 21348351.0 | 19054591.0 | 45973503.0 |
| fill_max_ns | 22315007.0 | 20496383.0 | 48431103.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
