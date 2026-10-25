# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 2000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 19699.0 | 15574.2 | 19784.4 |
| max_send_lag_ns | 6378690.0 | 2492492.0 | 17027566.0 |
| ack_p50_ns | 1471487.0 | 1267711.0 | 139853823.0 |
| ack_p90_ns | 5718015.0 | 3721215.0 | 216793087.0 |
| ack_p99_ns | 13664255.0 | 7118847.0 | 272105471.0 |
| ack_p99_9_ns | 16269311.0 | 8765439.0 | 299630591.0 |
| ack_max_ns | 17563647.0 | 9306111.0 | 304611327.0 |
| fill_p50_ns | 1846271.0 | 1604607.0 | 140247039.0 |
| fill_p90_ns | 5935103.0 | 4112383.0 | 217055231.0 |
| fill_p99_ns | 13705215.0 | 7430143.0 | 272105471.0 |
| fill_p99_9_ns | 16277503.0 | 8921087.0 | 299630591.0 |
| fill_max_ns | 18071551.0 | 10739711.0 | 304611327.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
