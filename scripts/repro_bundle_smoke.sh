#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Twenty simulator runs -> recorded concurrent multi-symbol bundles -> offline replay.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
mkdir -p target bin
log_dir="$(mktemp -d "$repo_root/target/repro-bundle.XXXXXX")"
export FXP_JWT_SECRET="$(openssl rand -hex 32)"
export FXP_JWT_ISSUER=repro-smoke FXP_JWT_AUDIENCE=repro-gateway FXP_JWT_KID=repro-key
export FXP_GW_TCP_ADDR=127.0.0.1:8082 FXP_GW_WS_ADDR=127.0.0.1:8081
export FXP_CLIENT_TLS=false FXP_TLS_REQUIRED=false
unset FXP_TLS_CERT_FILE FXP_TLS_KEY_FILE FXP_TLS_CLIENT_CA_FILE
unset FXP_CLIENT_TLS_CA_FILE FXP_CLIENT_TLS_CERT_FILE FXP_CLIENT_TLS_KEY_FILE FXP_CLIENT_TLS_SERVER_NAME
unset FXP_CORE_AUTH_TOKEN FXP_EVENT_LOG FXP_SESSION_SCHEDULE FXP_SYMBOL_CONFIG FXP_SIM_GATEWAY_ADDR
unset FXP_JWT_PREVIOUS_SECRET FXP_JWT_PREVIOUS_KID
export FXP_RUN_META_JSON='{"seed":7,"participants":4,"symbols":2}'
for port in 8080 8081 8082; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        printf 'error: port %s already in use\n' "$port" >&2; exit 1
    fi
done
(cd fxp_core && cargo build --release --locked)
(cd go_gateway && go build -o ../bin/go_gateway . && go build -o ../bin/simday ./simulation)
cleanup() {
    for pid in $(jobs -pr); do kill -TERM "$pid" 2>/dev/null || true; done
    wait 2>/dev/null || true
}
trap cleanup EXIT
wait_for_port() {
    for _ in $(seq 1 100); do
        (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return 0
        sleep 0.1
    done
    printf 'error: listener :%s unavailable (logs: %s)\n' "$1" "$log_dir" >&2; exit 1
}
(cd fxp_core && cargo test --lib --locked --no-run)
for iteration in $(seq 1 20); do
    run_dir="$log_dir/run-$iteration"
    mkdir "$run_dir"
    export FXP_RUN_RECORD="$run_dir/bundle"
    fxp_core/target/release/fxp_core >"$run_dir/core.log" 2>&1 &
    core_pid=$!; wait_for_port 8080
    ./bin/go_gateway >"$run_dir/gateway.log" 2>&1 &
    gateway_pid=$!; wait_for_port 8082
    timeout 45 ./bin/simday -duration 5 -symbols 2 -participants 4 -seed 7 >"$run_dir/simday.log" 2>&1
    grep -q 'All participants connected' "$run_dir/simday.log"
    grep -Eq 'Orders filled: +[1-9][0-9]*' "$run_dir/simday.log"
    kill -TERM "$gateway_pid"; wait "$gateway_pid"
    kill -TERM "$core_pid"; wait "$core_pid"
    # Test-only API driver, deliberately no user CLI in this PR.
    (cd fxp_core && FXP_TEST_SIM_BUNDLE="$FXP_RUN_RECORD" cargo test --lib --locked -- --exact reproducibility::tests::verify_simulator_bundle --nocapture) >"$run_dir/replay.log" 2>&1
    printf 'PASS simulator recording/replay %s/20\n' "$iteration"
done
printf 'PASS twenty recorded simulator runs: 4 sessions, 2 symbols, independent offline replay (logs: %s)\n' "$log_dir"
