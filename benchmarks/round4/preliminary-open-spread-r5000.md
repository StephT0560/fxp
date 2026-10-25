# Live FXP benchmark

Mode: **open**; 10 connections; 1000 measured + 100 warmup orders/connection; 5 repetitions; open-loop rate 500 orders/s/connection (ignored in closed mode).

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
| orders_per_second | 4981.4 | 4900.7 | 4990.5 |
| max_send_lag_ns | 6427722.0 | 2468400.0 | 95477400.0 |
| ack_p50_ns | 1226751.0 | 1154047.0 | 1658879.0 |
| ack_p90_ns | 3416063.0 | 2582527.0 | 13484031.0 |
| ack_p99_ns | 17711103.0 | 5922815.0 | 101384191.0 |
| ack_p99_9_ns | 31145983.0 | 8740863.0 | 123469823.0 |
| ack_max_ns | 33439743.0 | 10321919.0 | 129826815.0 |
| fill_p50_ns | 2777087.0 | 2680831.0 | 3039231.0 |
| fill_p90_ns | 4993023.0 | 4247551.0 | 14303231.0 |
| fill_p99_ns | 17891327.0 | 7057407.0 | 103350271.0 |
| fill_p99_9_ns | 31473663.0 | 9142271.0 | 125435903.0 |
| fill_max_ns | 33472511.0 | 10338303.0 | 131858431.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
