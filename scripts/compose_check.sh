#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Disposable default secure Compose stack, verified through all three listeners.
# Then core is disrupted (compose stop + start, compose restart, and a killed
# core process brought back by its restart policy); after each, the same smoke
# must pass again within FXP_RESILIENCE_DEADLINE seconds (default 30).
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
build_flag=--build
if [[ "${1:-}" == --no-build && "$#" == 1 ]]; then
    build_flag=--no-build
elif [[ "$#" != 0 ]]; then
    printf 'usage: %s [--no-build]\n' "$0" >&2; exit 1
fi
mkdir -p target
FXP_CERTS_DIR="$(mktemp -d "$repo_root/target/compose-certs.XXXXXX")"
export FXP_CERTS_DIR
export FXP_CERT_GID="$(id -g)"
export FXP_JWT_SECRET="$(openssl rand -hex 32)"
export FXP_CORE_AUTH_TOKEN="$(openssl rand -hex 32)"
export FXP_FIX_CREDENTIALS="COMPOSESMOKE:$(openssl rand -hex 24)"
# Explicit container paths so host TLS client settings cannot leak into services.
export FXP_TLS_CERT_FILE=/etc/fxp/certs/server.crt FXP_TLS_KEY_FILE=/etc/fxp/certs/server.key
export FXP_TLS_CLIENT_CA_FILE=/etc/fxp/certs/ca.crt
export FXP_GATEWAY_TLS_CA_FILE=/etc/fxp/certs/ca.crt
export FXP_GATEWAY_TLS_CERT_FILE=/etc/fxp/certs/client.crt
export FXP_GATEWAY_TLS_KEY_FILE=/etc/fxp/certs/client.key
export FXP_GATEWAY_TLS_SERVER_NAME=localhost
project="fxp-secure-check-$$"
compose=(docker compose --env-file /dev/null -f docker-compose.yml -p "$project")
cleanup() {
    "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$FXP_CERTS_DIR"
}
trap cleanup EXIT
make dev-certs
"${compose[@]}" up "$build_flag" --wait --wait-timeout 90
python3 scripts/compose_smoke.py

deadline="${FXP_RESILIENCE_DEADLINE:-30}"
fail() {
    printf 'FAIL %s\n' "$1" >&2
    docker ps -a --filter "label=com.docker.compose.project=$project" --format '{{.Names}} {{.Status}}' >&2
    for service in core gateway bridge; do
        printf -- '--- %s (last 15 lines)\n' "$service" >&2
        "${compose[@]}" logs --no-color --tail 15 "$service" 2>&1 | sed -E 's/eyJ[A-Za-z0-9._-]+/[JWT]/g' >&2 || true
    done
    exit 1
}
recovers() {
    local label="$1" started=$SECONDS
    until python3 scripts/compose_smoke.py >/dev/null 2>&1; do
        (( SECONDS - started < deadline )) || fail "$label: stack did not recover within ${deadline}s"
        sleep 1
    done
    printf 'PASS %s: listeners and FIX cross back after %ss\n' "$label" "$((SECONDS - started))"
}
kill_core_process() {
    # Kill fxp_core inside the container (not `docker kill`, which counts as a
    # manual stop); docker-init exits and the restart policy brings core back.
    local id restarts
    id="$("${compose[@]}" ps -q core)"
    restarts="$(docker inspect -f '{{.RestartCount}}' "$id")"
    docker exec "$id" sh -c \
        'for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = fxp_core ] && kill -KILL "${p#/proc/}"; done; true'
    for _ in $(seq 1 "$deadline"); do
        [[ "$(docker inspect -f '{{.RestartCount}}' "$id")" -gt "$restarts" ]] && return 0
        sleep 1
    done
    fail "killed core was not restarted by its restart policy"
}
"${compose[@]}" stop core >/dev/null && "${compose[@]}" start core >/dev/null
recovers "compose stop core && start core"
"${compose[@]}" restart core >/dev/null
recovers "compose restart core"
kill_core_process
recovers "killed core process (restart policy)"
echo "PASS secure Compose resilience: core disruptions recovered without manual action"
