#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Disrupt core in a running demo stack (`make demo`) and require the console to
# return to live, end to end, within FXP_RESILIENCE_DEADLINE seconds (default 30).
# Scenarios: compose stop + start, compose restart, and a killed core process
# brought back by its restart policy. Any failure prints container state and
# log tails and exits non-zero; a stack that looks healthy but is dead fails.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
deadline="${FXP_RESILIENCE_DEADLINE:-30}"
compose=(docker compose --env-file .env.demo -p fxp-demo -f docker-compose.dev.yml -f docker-compose.demo.yml --profile dev --profile demo)

fail() {
    printf 'FAIL %s\n' "$1" >&2
    docker ps -a --filter label=com.docker.compose.project=fxp-demo --format '{{.Names}} {{.Status}}' >&2
    for service in core gateway demo_api; do
        printf -- '--- %s (last 15 lines)\n' "$service" >&2
        # Logs never contain the JWT secret; redact any bearer token defensively.
        "${compose[@]}" logs --no-color --tail 15 "$service" 2>&1 | sed -E 's/eyJ[A-Za-z0-9._-]+/[JWT]/g' >&2 || true
    done
    exit 1
}

check_live() {
    local label="$1" started=$SECONDS
    if ! (cd web && FXP_CONSOLE_URL=http://127.0.0.1:3000 node scripts/live-probe.mjs "$deadline"); then
        fail "$label: console not live within ${deadline}s"
    fi
    printf 'PASS %s: live again after %ss\n' "$label" "$((SECONDS - started))"
}

kill_core_process() {
    # Kill fxp_core inside the container (not `docker kill`, which counts as a
    # manual stop); docker-init exits and the restart policy brings core back.
    docker exec "$("${compose[@]}" ps -q core)" sh -c \
        'for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = fxp_core ] && kill -KILL "${p#/proc/}"; done; true'
}

wait_core_restarted() {
    local id restarts=$1
    id="$("${compose[@]}" ps -aq core)"
    for _ in $(seq 1 "$deadline"); do
        [[ "$(docker inspect -f '{{.RestartCount}}' "$id")" -gt "$restarts" ]] && return 0
        sleep 1
    done
    fail "killed core was not restarted by its restart policy"
}

if [[ "${1:-}" == --kill-core ]]; then
    restarts="$(docker inspect -f '{{.RestartCount}}' "$("${compose[@]}" ps -q core)")"
    kill_core_process
    wait_core_restarted "$restarts"
    exit 0
fi

check_live "baseline (fresh login)"

# One page held open across every disruption: it must leave "live" while core
# is down and acknowledge the first order after each recovery, without reload.
compose_cmd="cd $(printf %q "$repo_root") && $(printf '%q ' "${compose[@]}")"
export FXP_STOP="$compose_cmd stop core >/dev/null"
export FXP_START="$compose_cmd start core >/dev/null"
export FXP_RESTART="$compose_cmd restart core >/dev/null"
export FXP_KILL="bash '$repo_root/scripts/demo_resilience_check.sh' --kill-core"
if ! (cd web && FXP_CONSOLE_URL=http://127.0.0.1:3000 node scripts/resilience-probe.mjs "$deadline"); then
    fail "held-open console did not ride through core disruptions"
fi

check_live "fresh login after disruptions"

echo "PASS demo resilience: console followed core down and back after every disruption"
