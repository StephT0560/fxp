# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 9949.1 | 9895.9 | 9954.4 |
| max_send_lag_ns | 2644836.0 | 2484335.0 | 11637551.0 |
| ack_p50_ns | 942591.0 | 937983.0 | 1229823.0 |
| ack_p90_ns | 1723391.0 | 1671167.0 | 4345855.0 |
| ack_p99_ns | 5722111.0 | 3293183.0 | 22577151.0 |
| ack_p99_9_ns | 9355263.0 | 5320703.0 | 26509311.0 |
| ack_max_ns | 10387455.0 | 5996543.0 | 27475967.0 |
| fill_p50_ns | 1583103.0 | 1556479.0 | 1867775.0 |
| fill_p90_ns | 2574335.0 | 2490367.0 | 4825087.0 |
| fill_p99_ns | 6193151.0 | 3921919.0 | 22872063.0 |
| fill_p99_9_ns | 10354687.0 | 6213631.0 | 26542079.0 |
| fill_max_ns | 11345919.0 | 6717439.0 | 27525119.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
