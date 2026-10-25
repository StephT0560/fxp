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
| orders_per_second | 19966.9 | 19940.1 | 19972.0 |
| max_send_lag_ns | 10803069.0 | 10124929.0 | 31545458.0 |
| ack_p50_ns | 1289215.0 | 1219583.0 | 1413119.0 |
| ack_p90_ns | 5095423.0 | 3778559.0 | 6344703.0 |
| ack_p99_ns | 18448383.0 | 9814015.0 | 130154495.0 |
| ack_p99_9_ns | 45285375.0 | 17301503.0 | 154271743.0 |
| ack_max_ns | 57114623.0 | 24182783.0 | 176553983.0 |
| fill_p50_ns | 1650687.0 | 1531903.0 | 1753087.0 |
| fill_p90_ns | 5378047.0 | 4012031.0 | 6672383.0 |
| fill_p99_ns | 18694143.0 | 10092543.0 | 130285567.0 |
| fill_p99_9_ns | 45645823.0 | 17694719.0 | 154271743.0 |
| fill_max_ns | 57114623.0 | 25673727.0 | 176947199.0 |

Latency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.
