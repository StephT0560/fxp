#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Isolated, non-threshold benchmark run. Output defaults to target/bench.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
out="${1:-$repo_root/target/bench}"
mode="${2:-full}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
if [[ "$mode" != full && "$mode" != --smoke ]]; then
    printf 'usage: %s [output-directory] [--smoke]\n' "$0" >&2
    exit 1
fi
cd "$repo_root"
for port in 8080 8081 8082; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        printf 'port %s is in use; benchmark requires an isolated stack\n' "$port" >&2
        exit 1
    fi
done
bash scripts/gen_proto.sh
(cd fxp_core && cargo build --release --locked)
(cd go_gateway && go build -trimpath -o "$out/go_gateway" . && go build -trimpath -o "$out/benchmark" ./benchmark)
export FXP_JWT_SECRET="${FXP_JWT_SECRET:-dev-secret-change-in-production}"
export FXP_GW_RATE="${FXP_GW_RATE:-2000}" FXP_GW_RATE_BURST="${FXP_GW_RATE_BURST:-4000}"
export GOMAXPROCS="${GOMAXPROCS:-2}" TOKIO_WORKER_THREADS="${TOKIO_WORKER_THREADS:-2}"
export RUST_LOG="${RUST_LOG:-warn}"
core_pid=""; gateway_pid=""
cleanup() {
    for pid in "$gateway_pid" "$core_pid"; do
        if [[ -n "$pid" ]]; then kill -TERM "$pid" 2>/dev/null || true; fi
    done
    for pid in "$gateway_pid" "$core_pid"; do
        if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
    done
}
trap cleanup EXIT
wait_port() {
    for _ in $(seq 1 100); do
        if (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then return; fi
        sleep 0.1
    done
    printf 'startup failed on port %s; inspect %s logs\n' "$1" "$out" >&2
    exit 1
}
env -u FXP_EVENT_LOG -u FXP_SESSION_SCHEDULE fxp_core/target/release/fxp_core >"$out/core.log" 2>&1 &
core_pid=$!; wait_port 8080
"$out/go_gateway" >"$out/gateway.log" 2>&1 &
gateway_pid=$!; wait_port 8082
{
    git rev-parse HEAD
    rustc --version
    go version
    uname -a
    for item in "core:$core_pid" "gateway:$gateway_pid" "runner:$$"; do
        printf '%s ' "$item"
        if command -v taskset >/dev/null; then taskset -pc "${item#*:}"; else printf 'affinity unavailable\n'; fi
    done
    printf 'GOMAXPROCS=%s TOKIO_WORKER_THREADS=%s FXP_GW_RATE=%s FXP_GW_RATE_BURST=%s RUST_LOG=%s\n' "$GOMAXPROCS" "$TOKIO_WORKER_THREADS" "$FXP_GW_RATE" "$FXP_GW_RATE_BURST" "$RUST_LOG"
    printf 'Persistence disabled; session schedule unset. Shared host, loopback; no explicit taskset pinning.\n'
} >"$out/build-environment.txt"
orders=1000; warmup=100; repetitions=5
if [[ "$mode" == --smoke ]]; then orders=20; warmup=10; repetitions=1; fi
for scenario in open-c1 open-c10 closed-c1; do
    clients=1; symbols=single; load=open
    if [[ "$scenario" == open-c10 ]]; then clients=10; symbols=spread; fi
    if [[ "$scenario" == closed-c1 ]]; then load=closed; fi
    "$out/benchmark" --mode "$load" --concurrency "$clients" --symbols "$symbols" \
        --rate 1000 --orders "$orders" --warmup "$warmup" --repetitions "$repetitions" \
        --pinning "no explicit pinning; component affinities/build versions in build-environment.txt" \
        --json "$out/$scenario.json" --markdown "$out/$scenario.md"
done
# Encoding must not contend with the running live benchmark processes.
cleanup
core_pid=""; gateway_pid=""
if [[ "$mode" != --smoke ]]; then
    criterion_dir="$(mktemp -d "$out/criterion.XXXXXX")"
    (cd fxp_core && CRITERION_HOME="$criterion_dir" cargo bench --locked --bench encoding_benchmark -- \
        --sample-size 50 --warm-up-time 1 --measurement-time 2 --noplot) 2>&1 | tee "$out/encoding.log"
    python3 scripts/collect_encoding.py "$criterion_dir" "$out/encoding.json"
fi
