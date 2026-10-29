#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Native loopback stack, recorded commands, seeded Python bot, offline diagnosis.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
python="${FXP_REPRO_PYTHON:-python3}"
"$python" -c 'import fxp_client' 2>/dev/null || {
    echo 'Install the local SDK first: python3 -m pip install -e sdk/python (see docs/bot-workflow.md)' >&2; exit 1;
}
mkdir -p target bin
if [[ -n "${FXP_REPRO_DIR:-}" ]]; then
    mkdir -m 700 "$FXP_REPRO_DIR"  # refuse reuse, including incomplete older runs
    run_dir="$(cd "$FXP_REPRO_DIR" && pwd)"
else
    run_dir="$(mktemp -d "$repo_root/target/bot-repro.XXXXXX")"
fi
seed="${FXP_REPRO_SEED:-7}"
repro_test_client="${FXP_REPRO_TEST_CLIENT:-0}"
# The core currently binds :8080; do not attach to or stop an existing stack.
for port in 8080 8081 8082; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        printf 'error: port %s already in use; stop your local stack first\n' "$port" >&2; exit 1
    fi
done
# Run children with isolated FXP configuration, retaining normal PATH/build caches.
while IFS= read -r name; do unset "$name"; done < <(compgen -v FXP_)
export FXP_JWT_SECRET="$(openssl rand -hex 32)"
export FXP_JWT_ISSUER=bot-repro FXP_JWT_AUDIENCE=bot-gateway
export FXP_GW_TCP_ADDR=127.0.0.1:8082 FXP_GW_WS_ADDR=127.0.0.1:8081
export FXP_TLS_REQUIRED=false FXP_CLIENT_TLS=false
export FXP_RUN_RECORD="$run_dir/bundle"
export FXP_RUN_META_JSON="$("$python" -c 'import json,sys; print(json.dumps({"seed":int(sys.argv[1]),"example":"bot_repro","deliberate_bug":"remaining quantity mistaken for total after fill"}))' "$seed")"
export FXP_SYMBOL_CONFIG="$repo_root/examples/bot_repro/symbols.csv"
export RUST_LOG=warn
# Intentional local-only plaintext; never expose these disposable credentials.
core_pid=''; gateway_pid=''
stop() {
    local pid="$1" label="$2"
    if ! kill -0 "$pid" 2>/dev/null; then wait "$pid"; return; fi
    kill -TERM "$pid"
    for _ in $(seq 1 200); do
        if ! kill -0 "$pid" 2>/dev/null; then wait "$pid"; return; fi
        sleep 0.1
    done
    printf 'error: %s failed to shut down cleanly; bundle may be incomplete\n' "$label" >&2
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" || true
    return 1
}
cleanup() {
    [[ -z "$gateway_pid" ]] || stop "$gateway_pid" gateway || true
    [[ -z "$core_pid" ]] || stop "$core_pid" core || true
    printf 'Reproduction files: %s\n' "$run_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
wait_for_port() {
    local pid="$1" port="$2"
    for _ in $(seq 1 150); do
        kill -0 "$pid" 2>/dev/null || { echo "process exited; inspect $run_dir" >&2; return 1; }
        (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && return 0
        sleep 0.1
    done
    echo "listener :$port unavailable; inspect $run_dir" >&2; return 1
}
bash scripts/gen_proto.sh >"$run_dir/build.log" 2>&1
cargo build --release --locked --manifest-path fxp_core/Cargo.toml --bin fxp_core --bin fxp-replay >>"$run_dir/build.log" 2>&1
(cd go_gateway && go build -o ../bin/bot-repro-gateway .) >>"$run_dir/build.log" 2>&1
fxp_core/target/release/fxp_core >"$run_dir/core.log" 2>&1 & core_pid=$!
wait_for_port "$core_pid" 8080
./bin/bot-repro-gateway >"$run_dir/gateway.log" 2>&1 & gateway_pid=$!
wait_for_port "$gateway_pid" 8082
# Optional real SDK integration assertions on this same fresh stack (CI).
if [[ "${repro_test_client:-0}" == 1 ]]; then
    FXP_REPRO_TEST_CLIENT=1 "$python" -m unittest discover -s sdk/python/tests -p test_integration.py -v
fi
"$python" examples/bot_repro/scenario.py --seed "$seed" --result "$run_dir/scenario.json"
stop "$gateway_pid" gateway; gateway_pid=''
stop "$core_pid" core; core_pid=''
replay=fxp_core/target/release/fxp-replay
"$replay" verify "$run_dir/bundle" | tee "$run_dir/verify.txt"
order_id="$("$python" -c 'import json,sys;print(json.load(open(sys.argv[1]))["core_order_id"])' "$run_dir/scenario.json")"
"$replay" timeline "$run_dir/bundle" --order "$order_id" --owner demo-maker --symbol BOTTEST | tee "$run_dir/timeline.txt"
"$replay" explain "$run_dir/bundle" --order "$order_id" --owner demo-maker --symbol BOTTEST | tee "$run_dir/explain.txt"
"$replay" timeline "$run_dir/bundle" --order "$order_id" --owner demo-maker --symbol BOTTEST --json >"$run_dir/timeline.json"
"$python" examples/bot_repro/diagnose.py "$run_dir/timeline.json" "$run_dir/scenario.json" | tee "$run_dir/diagnosis.txt"
