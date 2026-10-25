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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 19945.0 | 19906.0 | 19960.4 |
| max_send_lag_ns | 21220993.0 | 17614805.0 | 169549948.0 |
| ack_p50_ns | 1439743.0 | 1241087.0 | 3766271.0 |
| ack_p90_ns | 9617407.0 | 4175871.0 | 167772159.0 |
| ack_p99_ns | 92340223.0 | 11010047.0 | 373555199.0 |
| ack_p99_9_ns | 122028031.0 | 26329087.0 | 423362559.0 |
| ack_max_ns | 141295615.0 | 35454975.0 | 443285503.0 |
| fill_p50_ns | 1825791.0 | 1556479.0 | 4075519.0 |
| fill_p90_ns | 9928703.0 | 4427775.0 | 168165375.0 |
| fill_p99_ns | 92733439.0 | 11223039.0 | 374079487.0 |
| fill_p99_9_ns | 122224639.0 | 26787839.0 | 423624703.0 |
| fill_max_ns | 141426687.0 | 35946495.0 | 443285503.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
