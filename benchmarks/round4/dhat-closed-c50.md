# Live FXP benchmark

Mode: **closed**; 50 connections; 100 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
- revision: 16c051471f09ac5c685be9c96ae2cb617a1b1495
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 5324.9 | 5187.2 | 5435.9 |
| max_send_lag_ns | 91098.0 | 50703.0 | 134397.0 |
| ack_p50_ns | 9273343.0 | 9101311.0 | 9355263.0 |
| ack_p90_ns | 10944511.0 | 10371071.0 | 11255807.0 |
| ack_p99_ns | 17219583.0 | 16809983.0 | 18055167.0 |
| ack_p99_9_ns | 19169279.0 | 18268159.0 | 20922367.0 |
| ack_max_ns | 19628031.0 | 18595839.0 | 21872639.0 |
| fill_p50_ns | 12820479.0 | 12623871.0 | 13590527.0 |
| fill_p90_ns | 19349503.0 | 19103743.0 | 20463615.0 |
| fill_p99_ns | 25411583.0 | 24788991.0 | 26804223.0 |
| fill_p99_9_ns | 27738111.0 | 27590655.0 | 31326207.0 |
| fill_max_ns | 31227903.0 | 28278783.0 | 32079871.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
