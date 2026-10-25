#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Disposable loopback core + gateway; secrets are environment-only, never traced.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
mkdir -p target bin
log_dir="$(mktemp -d "$repo_root/target/simday-smoke.XXXXXX")"
export FXP_JWT_SECRET="$(openssl rand -hex 32)"
export FXP_JWT_ISSUER=simday-smoke FXP_JWT_AUDIENCE=simday-gateway FXP_JWT_KID=simday-key
export FXP_GW_TCP_ADDR=127.0.0.1:8082 FXP_GW_WS_ADDR=127.0.0.1:8081
export FXP_CLIENT_TLS=false FXP_TLS_REQUIRED=false
unset FXP_TLS_CERT_FILE FXP_TLS_KEY_FILE FXP_TLS_CLIENT_CA_FILE
unset FXP_CLIENT_TLS_CA_FILE FXP_CLIENT_TLS_CERT_FILE FXP_CLIENT_TLS_KEY_FILE FXP_CLIENT_TLS_SERVER_NAME
unset FXP_CORE_AUTH_TOKEN FXP_EVENT_LOG FXP_SESSION_SCHEDULE FXP_SYMBOL_CONFIG FXP_SIM_GATEWAY_ADDR
unset FXP_JWT_PREVIOUS_SECRET FXP_JWT_PREVIOUS_KID
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
(cd fxp_core && exec ./target/release/fxp_core) >"$log_dir/core.log" 2>&1 &
wait_for_port 8080
./bin/go_gateway >"$log_dir/gateway.log" 2>&1 &
wait_for_port 8082
timeout 45 ./bin/simday -duration 5 -symbols 2 -participants 4 -seed 7 | tee "$log_dir/simday.log"
grep -q 'All participants connected' "$log_dir/simday.log"
grep -Eq 'Orders filled: +[1-9][0-9]*' "$log_dir/simday.log"
printf 'PASS simday: configured random secret + custom iss/aud/kid, trades observed (logs: %s)\n' "$log_dir"
