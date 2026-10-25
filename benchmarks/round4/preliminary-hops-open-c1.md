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
- revision: 3b606a32f2795276523e6c6f8c6e1fb7316c0c63
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 999.9 | 999.2 | 1000.2 |
| max_send_lag_ns | 1538978.0 | 1131126.0 | 4173665.0 |
| ack_p50_ns | 743423.0 | 712191.0 | 769023.0 |
| ack_p90_ns | 1211391.0 | 1204223.0 | 1258495.0 |
| ack_p99_ns | 2207743.0 | 1839103.0 | 2760703.0 |
| ack_p99_9_ns | 3332095.0 | 2248703.0 | 4276223.0 |
| ack_max_ns | 3465215.0 | 2408447.0 | 5230591.0 |
| fill_p50_ns | 1273855.0 | 1249279.0 | 1319935.0 |
| fill_p90_ns | 2140159.0 | 2089983.0 | 2217983.0 |
| fill_p99_ns | 2859007.0 | 2426879.0 | 3272703.0 |
| fill_p99_9_ns | 4214783.0 | 3194879.0 | 5201919.0 |
| fill_max_ns | 4481023.0 | 3428351.0 | 5697535.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
