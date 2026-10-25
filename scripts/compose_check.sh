#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Disposable default secure Compose stack, verified through all three listeners.
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
