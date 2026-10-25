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
| orders_per_second | 33278.0 | 29374.0 | 37050.7 |
| max_send_lag_ns | 13499687.0 | 3636560.0 | 15370778.0 |
| ack_p50_ns | 20971519.0 | 18841599.0 | 82051071.0 |
| ack_p90_ns | 60325887.0 | 27279359.0 | 122617855.0 |
| ack_p99_ns | 85458943.0 | 33210367.0 | 148766719.0 |
| ack_p99_9_ns | 98697215.0 | 36110335.0 | 159776767.0 |
| ack_max_ns | 101122047.0 | 37322751.0 | 161087487.0 |
| fill_p50_ns | 21118975.0 | 19021823.0 | 82182143.0 |
| fill_p90_ns | 60391423.0 | 27459583.0 | 122814463.0 |
| fill_p99_ns | 85524479.0 | 33374207.0 | 148766719.0 |
| fill_p99_9_ns | 98697215.0 | 36175871.0 | 159907839.0 |
| fill_max_ns | 101122047.0 | 37322751.0 | 161087487.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
