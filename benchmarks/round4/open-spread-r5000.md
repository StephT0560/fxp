# Live FXP benchmark

Mode: **open**; 10 connections; 2500 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 500 orders/s/connection (ignored in closed mode).

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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 4994.6 | 4989.3 | 4996.0 |
| max_send_lag_ns | 7333308.0 | 2926052.0 | 19101183.0 |
| ack_p50_ns | 969727.0 | 963071.0 | 990207.0 |
| ack_p90_ns | 1488895.0 | 1463295.0 | 1555455.0 |
| ack_p99_ns | 4907007.0 | 2977791.0 | 5828607.0 |
| ack_p99_9_ns | 18087935.0 | 5410815.0 | 30785535.0 |
| ack_max_ns | 24084479.0 | 5607423.0 | 38862847.0 |
| fill_p50_ns | 2486271.0 | 2445311.0 | 2516991.0 |
| fill_p90_ns | 3395583.0 | 3358719.0 | 3436543.0 |
| fill_p99_ns | 5939199.0 | 4452351.0 | 7315455.0 |
| fill_p99_9_ns | 18759679.0 | 5677055.0 | 31326207.0 |
| fill_max_ns | 24084479.0 | 7512063.0 | 38862847.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
