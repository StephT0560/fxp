# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 500 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 4988.2 | 4986.6 | 4991.4 |
| max_send_lag_ns | 2210239.0 | 1736656.0 | 4803108.0 |
| ack_p50_ns | 990207.0 | 969727.0 | 1025535.0 |
| ack_p90_ns | 1511423.0 | 1435647.0 | 1725439.0 |
| ack_p99_ns | 3450879.0 | 2785279.0 | 5967871.0 |
| ack_p99_9_ns | 12279807.0 | 4616191.0 | 15785983.0 |
| ack_max_ns | 13959167.0 | 5951487.0 | 17711103.0 |
| fill_p50_ns | 2439167.0 | 2426879.0 | 2486271.0 |
| fill_p90_ns | 3409919.0 | 3373055.0 | 3553279.0 |
| fill_p99_ns | 4894719.0 | 4534271.0 | 7393279.0 |
| fill_p99_9_ns | 12926975.0 | 6172671.0 | 17596415.0 |
| fill_max_ns | 15474687.0 | 7946239.0 | 19824639.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
