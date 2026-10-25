# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 8000 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 44819.9 | 43899.1 | 47180.3 |
| max_send_lag_ns | 7437031.0 | 5863033.0 | 10300756.0 |
| ack_p50_ns | 89391103.0 | 81330175.0 | 96272383.0 |
| ack_p90_ns | 119275519.0 | 111869951.0 | 125566975.0 |
| ack_p99_ns | 138936319.0 | 130482175.0 | 141688831.0 |
| ack_p99_9_ns | 145752063.0 | 136445951.0 | 147587071.0 |
| ack_max_ns | 146931711.0 | 137625599.0 | 148766719.0 |
| fill_p50_ns | 89522175.0 | 81461247.0 | 96403455.0 |
| fill_p90_ns | 119406591.0 | 111869951.0 | 125632511.0 |
| fill_p99_ns | 138936319.0 | 130482175.0 | 141950975.0 |
| fill_p99_9_ns | 145752063.0 | 136445951.0 | 147718143.0 |
| fill_max_ns | 146931711.0 | 137625599.0 | 148897791.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
