# Live FXP benchmark

Mode: **open**; 1 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 1000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 999.3 | 998.4 | 1000.3 |
| max_send_lag_ns | 2003931.0 | 1452290.0 | 110666999.0 |
| ack_p50_ns | 711167.0 | 680447.0 | 857087.0 |
| ack_p90_ns | 1243135.0 | 1172479.0 | 19726335.0 |
| ack_p99_ns | 2195455.0 | 1560575.0 | 108527615.0 |
| ack_p99_9_ns | 3870719.0 | 2084863.0 | 117440511.0 |
| ack_max_ns | 4333567.0 | 2301951.0 | 118423551.0 |
| fill_p50_ns | 1302527.0 | 1224703.0 | 1514495.0 |
| fill_p90_ns | 2142207.0 | 2045951.0 | 20725759.0 |
| fill_p99_ns | 2969599.0 | 2396159.0 | 109510655.0 |
| fill_p99_9_ns | 4374527.0 | 2949119.0 | 118423551.0 |
| fill_max_ns | 5337087.0 | 3088383.0 | 119406591.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
