# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 8000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 37721.8 | 34614.1 | 44059.8 |
| max_send_lag_ns | 11599735.0 | 7696607.0 | 24049867.0 |
| ack_p50_ns | 127664127.0 | 96206847.0 | 151912447.0 |
| ack_p90_ns | 152436735.0 | 125370367.0 | 186384383.0 |
| ack_p99_ns | 169738239.0 | 144834559.0 | 204996607.0 |
| ack_p99_9_ns | 177078271.0 | 150208511.0 | 210894847.0 |
| ack_max_ns | 178257919.0 | 151126015.0 | 212074495.0 |
| fill_p50_ns | 127795199.0 | 96272383.0 | 151912447.0 |
| fill_p90_ns | 152567807.0 | 125435903.0 | 186384383.0 |
| fill_p99_ns | 169738239.0 | 144965631.0 | 204996607.0 |
| fill_p99_9_ns | 177078271.0 | 150470655.0 | 210894847.0 |
| fill_max_ns | 178257919.0 | 151519231.0 | 212074495.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
