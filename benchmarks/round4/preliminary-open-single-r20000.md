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
| orders_per_second | 19784.7 | 19665.5 | 19790.8 |
| max_send_lag_ns | 2177298.0 | 1889722.0 | 3045030.0 |
| ack_p50_ns | 1117183.0 | 971775.0 | 1184767.0 |
| ack_p90_ns | 2844671.0 | 2131967.0 | 2959359.0 |
| ack_p99_ns | 7479295.0 | 3934207.0 | 14385151.0 |
| ack_p99_9_ns | 9814015.0 | 5709823.0 | 16375807.0 |
| ack_max_ns | 10567679.0 | 6230015.0 | 16891903.0 |
| fill_p50_ns | 1459199.0 | 1375231.0 | 1484799.0 |
| fill_p90_ns | 3176447.0 | 2500607.0 | 3276799.0 |
| fill_p99_ns | 7892991.0 | 4526079.0 | 14647295.0 |
| fill_p99_9_ns | 10084351.0 | 6365183.0 | 16875519.0 |
| fill_max_ns | 10797055.0 | 7643135.0 | 17219583.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
