# Live FXP benchmark

Mode: **open**; 10 connections; 10000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 2000 orders/s/connection (ignored in closed mode).

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
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 19966.9 | 19962.8 | 19971.9 |
| max_send_lag_ns | 18941670.0 | 7586651.0 | 28815732.0 |
| ack_p50_ns | 1224703.0 | 1191935.0 | 1245183.0 |
| ack_p90_ns | 3905535.0 | 3432447.0 | 4251647.0 |
| ack_p99_ns | 16637951.0 | 12247039.0 | 38993919.0 |
| ack_p99_9_ns | 31506431.0 | 24936447.0 | 58556415.0 |
| ack_max_ns | 38240255.0 | 33079295.0 | 65634303.0 |
| fill_p50_ns | 1562623.0 | 1538047.0 | 1613823.0 |
| fill_p90_ns | 4247551.0 | 3745791.0 | 4583423.0 |
| fill_p99_ns | 16908287.0 | 12500991.0 | 39321599.0 |
| fill_p99_9_ns | 31539199.0 | 25034751.0 | 58949631.0 |
| fill_max_ns | 38273023.0 | 33095679.0 | 65765375.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
