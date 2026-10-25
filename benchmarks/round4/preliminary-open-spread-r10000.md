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
| orders_per_second | 9892.8 | 9656.4 | 9918.0 |
| max_send_lag_ns | 8469421.0 | 3466641.0 | 24629557.0 |
| ack_p50_ns | 1551359.0 | 1256447.0 | 5173247.0 |
| ack_p90_ns | 6123519.0 | 3325951.0 | 20152319.0 |
| ack_p99_ns | 24264703.0 | 6971391.0 | 32358399.0 |
| ack_p99_9_ns | 31211519.0 | 9125887.0 | 37519359.0 |
| ack_max_ns | 32325631.0 | 10182655.0 | 41910271.0 |
| fill_p50_ns | 2238463.0 | 1825791.0 | 5677055.0 |
| fill_p90_ns | 6635519.0 | 3909631.0 | 20561919.0 |
| fill_p99_ns | 24887295.0 | 7331839.0 | 32686079.0 |
| fill_p99_9_ns | 31817727.0 | 9461759.0 | 37847039.0 |
| fill_max_ns | 32800767.0 | 10338303.0 | 41943039.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
