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
- revision: c5210ec11d372721043c4b75ae7606d71beb9e44
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 19960.0 | 19956.1 | 19971.1 |
| max_send_lag_ns | 27748616.0 | 9877690.0 | 34430311.0 |
| ack_p50_ns | 1327103.0 | 1198079.0 | 1499135.0 |
| ack_p90_ns | 7798783.0 | 3835903.0 | 18333695.0 |
| ack_p99_ns | 46333951.0 | 16629759.0 | 134348799.0 |
| ack_p99_9_ns | 60358655.0 | 36536319.0 | 170000383.0 |
| ack_max_ns | 69533695.0 | 45252607.0 | 180223999.0 |
| fill_p50_ns | 1693695.0 | 1531903.0 | 1853439.0 |
| fill_p90_ns | 8040447.0 | 4128767.0 | 18579455.0 |
| fill_p99_ns | 46596095.0 | 16990207.0 | 134742015.0 |
| fill_p99_9_ns | 60522495.0 | 36765695.0 | 170393599.0 |
| fill_max_ns | 69533695.0 | 45776895.0 | 180355071.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
