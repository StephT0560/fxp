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
- revision: 4f19b7fe2b1e175063cba93ceaff070dd75c7751
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 1507.5 | 1442.6 | 1523.0 |
| max_send_lag_ns | 200521.0 | 69614.0 | 684161.0 |
| ack_p50_ns | 6463487.0 | 6356991.0 | 6483967.0 |
| ack_p90_ns | 10518527.0 | 10346495.0 | 11018239.0 |
| ack_p99_ns | 14696447.0 | 13836287.0 | 18776063.0 |
| ack_p99_9_ns | 24936447.0 | 19922943.0 | 43089919.0 |
| ack_max_ns | 39616511.0 | 20938751.0 | 61079551.0 |
| fill_p50_ns | 9175039.0 | 9093119.0 | 9560063.0 |
| fill_p90_ns | 16080895.0 | 15982591.0 | 17088511.0 |
| fill_p99_ns | 22708223.0 | 21463039.0 | 27197439.0 |
| fill_p99_9_ns | 39190527.0 | 26476543.0 | 60620799.0 |
| fill_max_ns | 48398335.0 | 31768575.0 | 79298559.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
