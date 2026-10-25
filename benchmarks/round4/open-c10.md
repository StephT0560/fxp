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
- revision: f6148617d493b9aab2df2181130e6714b8ebe89b
- rustc_local: rustc 1.99.1 (58cf8f9c3 2026-10-13)

| Metric | Median of runs | Min | Max |
|---|---:|---:|---:|
| orders_per_second | 9946.6 | 9939.5 | 9959.2 |
| max_send_lag_ns | 2064987.0 | 1737523.0 | 2298285.0 |
| ack_p50_ns | 947711.0 | 919039.0 | 961535.0 |
| ack_p90_ns | 1683455.0 | 1618943.0 | 1768447.0 |
| ack_p99_ns | 3661823.0 | 3104767.0 | 13443071.0 |
| ack_p99_9_ns | 6574079.0 | 4300799.0 | 17694719.0 |
| ack_max_ns | 8278015.0 | 5144575.0 | 19922943.0 |
| fill_p50_ns | 1588223.0 | 1550335.0 | 1610751.0 |
| fill_p90_ns | 2498559.0 | 2476031.0 | 2629631.0 |
| fill_p99_ns | 4427775.0 | 3692543.0 | 13877247.0 |
| fill_p99_9_ns | 7401471.0 | 5001215.0 | 18448383.0 |
| fill_max_ns | 8290303.0 | 6914047.0 | 20922367.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
