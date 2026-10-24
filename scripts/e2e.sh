#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Builds and starts core -> gateway -> fix_bridge, runs the Python FIX suite,
# then stops the stack. Exits non-zero if any scenario fails.
set -euo pipefail

if ! command -v go >/dev/null 2>&1 && [[ -x /usr/local/go/bin/go ]]; then
    export PATH="/usr/local/go/bin:$PATH"
fi
if ! command -v go >/dev/null 2>&1; then
    printf 'error: Go is required (not on PATH or in /usr/local/go/bin)\n' >&2
    exit 1
fi

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="${E2E_LOG_DIR:-$repo_root/target/e2e}"
mkdir -p "$log_dir" "$repo_root/bin"

export FXP_JWT_SECRET="${FXP_JWT_SECRET:-dev-secret-change-in-production}"
export FXP_FIX_PASSWORD="${FXP_FIX_PASSWORD:-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
export FXP_FIX_CREDENTIALS="${FXP_FIX_CREDENTIALS:-TESTFIRM01:$FXP_FIX_PASSWORD,VICTIM01:$FXP_FIX_PASSWORD,ATTACKER01:$FXP_FIX_PASSWORD,SEQCHECK01:$FXP_FIX_PASSWORD,MDCHECK01:$FXP_FIX_PASSWORD,DURABLE01:$FXP_FIX_PASSWORD,MASSCXL01:$FXP_FIX_PASSWORD,CODTEST01:$FXP_FIX_PASSWORD,CODCHK01:$FXP_FIX_PASSWORD}"
if [[ -z "${FXP_FIX_STORE_DIR:-}" ]]; then
    export FXP_FIX_STORE_DIR="$log_dir/fix_store"
    rm -rf "$FXP_FIX_STORE_DIR"
fi
export FXP_E2E_DURABLE_STATE="$log_dir/durable.state"
rm -f "$FXP_E2E_DURABLE_STATE"

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

(cd "$repo_root/fxp_core" && exec env -u FXP_EVENT_LOG ./target/release/fxp_core) >"$log_dir/core.log" 2>&1 &
core_pid=$!; pids+=("$core_pid"); wait_for_port 8080
(cd "$repo_root/go_gateway" && exec "$repo_root/bin/go_gateway") >"$log_dir/gateway.log" 2>&1 &
gateway_pid=$!; pids+=("$gateway_pid"); wait_for_port 8082
start_bridge() {
    (cd "$repo_root/fix_bridge" && exec "$repo_root/bin/fix_bridge") >>"$log_dir/bridge.log" 2>&1 &
    bridge_pid=$!
    pids+=("$bridge_pid")
    wait_for_port 8083
}
: >"$log_dir/bridge.log"
start_bridge

python3 "$repo_root/fix_test_client/fix_client.py" | tee "$log_dir/fix_client.log"
grep -Eq 'Results: ([1-9][0-9]*)/\1 passed$' "$log_dir/fix_client.log"

# Cancel-on-disconnect: reset a COD session's TCP socket; its resting and stop
# orders must leave the book and the cancels must be resent after reconnect.
python3 "$repo_root/fix_test_client/fix_client.py" --sender CODTEST01 --test cod_disconnect | tee "$log_dir/cod.log"

# Kill the bridge (SIGKILL) mid-session and restart it: the durable FIX store
# must resume DURABLE01's sequence numbers and serve a ResendRequest.
durable() { python3 "$repo_root/fix_test_client/fix_client.py" --sender DURABLE01 --test "$1"; }
durable durable_before_kill >"$log_dir/durable_before.log" 2>&1 &
client_pid=$!
for _ in $(seq 1 100); do [[ -s "$FXP_E2E_DURABLE_STATE" ]] && break; sleep 0.1; done
kill -9 "$bridge_pid"
wait "$bridge_pid" 2>/dev/null || true
start_bridge
client_ok=0
wait "$client_pid" || client_ok=$?
cat "$log_dir/durable_before.log"
[[ $client_ok -eq 0 ]]
durable durable_after_restart | tee "$log_dir/durable_after.log"

# Exercise core persistence directly: gateway orderClientMap is intentionally
# not recovered. Never reuse an operator's FXP_EVENT_LOG for this test.
kill "$bridge_pid" "$gateway_pid" "$core_pid"
wait "$bridge_pid" "$gateway_pid" "$core_pid" 2>/dev/null || true
event_log="$log_dir/core-restart-events.log"
rm -f "$event_log"
start_persistent_core() {
    (cd "$repo_root/fxp_core" && exec env -u FXP_SESSION_SCHEDULE FXP_EVENT_LOG="$event_log" ./target/release/fxp_core) >>"$log_dir/core-persistence.log" 2>&1 &
    core_pid=$!
    pids+=("$core_pid")
    wait_for_port 8080
}
start_persistent_core
(cd "$repo_root/go_gateway" && go run ./test_clients/persistence_client seed)
kill -9 "$core_pid"
wait "$core_pid" 2>/dev/null || true
start_persistent_core
(cd "$repo_root/go_gateway" && go run ./test_clients/persistence_client verify)
