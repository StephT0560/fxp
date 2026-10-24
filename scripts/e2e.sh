#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Builds and starts core -> gateway -> fix_bridge, runs the Python FIX suite,
# then stops the stack. Exits non-zero if any scenario fails.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="${E2E_LOG_DIR:-$repo_root/target/e2e}"
mkdir -p "$log_dir" "$repo_root/bin"

export FXP_JWT_SECRET="${FXP_JWT_SECRET:-dev-secret-change-in-production}"
export FXP_FIX_PASSWORD="${FXP_FIX_PASSWORD:-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
export FXP_FIX_CREDENTIALS="${FXP_FIX_CREDENTIALS:-TESTFIRM01:$FXP_FIX_PASSWORD,VICTIM01:$FXP_FIX_PASSWORD,ATTACKER01:$FXP_FIX_PASSWORD,SEQCHECK01:$FXP_FIX_PASSWORD}"

for port in 8080 8081 8082 8083; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        printf 'error: port %s already in use; stop the old stack first\n' "$port" >&2
        exit 1
    fi
done

(cd "$repo_root/fxp_core" && cargo build --release --locked)
(cd "$repo_root/go_gateway" && go build -o "$repo_root/bin/go_gateway" .)
(cd "$repo_root/fix_bridge" && go build -o "$repo_root/bin/fix_bridge" .)

pids=()
cleanup() { for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT

wait_for_port() {
    for _ in $(seq 1 100); do
        (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return 0
        sleep 0.1
    done
    printf 'error: nothing listening on :%s (see %s)\n' "$1" "$log_dir" >&2
    exit 1
}

(cd "$repo_root/fxp_core" && exec ./target/release/fxp_core) >"$log_dir/core.log" 2>&1 &
pids+=($!); wait_for_port 8080
(cd "$repo_root/go_gateway" && exec "$repo_root/bin/go_gateway") >"$log_dir/gateway.log" 2>&1 &
pids+=($!); wait_for_port 8082
(cd "$repo_root/fix_bridge" && exec "$repo_root/bin/fix_bridge") >"$log_dir/bridge.log" 2>&1 &
pids+=($!); wait_for_port 8083

python3 "$repo_root/fix_test_client/fix_client.py" | tee "$log_dir/fix_client.log"
grep -Eq 'Results: ([1-9][0-9]*)/\1 passed$' "$log_dir/fix_client.log"
