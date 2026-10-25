# Live FXP benchmark

Mode: **closed**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 8900.2 | 8792.8 | 20919.9 |
| max_send_lag_ns | 217976.0 | 116273.0 | 328948.0 |
| ack_p50_ns | 1017343.0 | 379391.0 | 1022975.0 |
| ack_p90_ns | 1680383.0 | 748543.0 | 1702911.0 |
| ack_p99_ns | 2912255.0 | 2062335.0 | 3250175.0 |
| ack_p99_9_ns | 4943871.0 | 3237887.0 | 6729727.0 |
| ack_max_ns | 7225343.0 | 3512319.0 | 15007743.0 |
| fill_p50_ns | 1517567.0 | 595967.0 | 1531903.0 |
| fill_p90_ns | 2701311.0 | 1191935.0 | 2734079.0 |
| fill_p99_ns | 4481023.0 | 2844671.0 | 4644863.0 |
| fill_p99_9_ns | 7229439.0 | 4114431.0 | 14876671.0 |
| fill_max_ns | 9666559.0 | 5656575.0 | 16244735.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
