#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# All three external listeners use mTLS; core connections require a distinct token.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$repo_root/target"
certs="$(mktemp -d "$repo_root/target/tls-certs.XXXXXX")"
trap 'rm -rf "$certs"' EXIT
bash "$repo_root/scripts/gen_test_certs.sh" "$certs"
export FXP_TLS_CERT_FILE="$certs/server.crt" FXP_TLS_KEY_FILE="$certs/server.key"
export FXP_TLS_CLIENT_CA_FILE="$certs/ca.crt" FXP_TLS_REQUIRED=true
export FXP_GATEWAY_TLS=true FXP_GATEWAY_TLS_CA_FILE="$certs/ca.crt"
export FXP_GATEWAY_TLS_CERT_FILE="$certs/client.crt" FXP_GATEWAY_TLS_KEY_FILE="$certs/client.key"
export FXP_GATEWAY_TLS_SERVER_NAME=localhost
export FXP_CLIENT_TLS=true FXP_CLIENT_TLS_CA_FILE="$certs/ca.crt"
export FXP_CLIENT_TLS_CERT_FILE="$certs/client.crt" FXP_CLIENT_TLS_KEY_FILE="$certs/client.key"
export FXP_CLIENT_TLS_SERVER_NAME=localhost
export FXP_CORE_AUTH_TOKEN="$(openssl rand -hex 32)"
export FXP_E2E_TLS_CERT_DIR="$certs"
export E2E_LOG_DIR="${E2E_LOG_DIR:-$repo_root/target/e2e-tls}"
bash "$repo_root/scripts/e2e.sh"
bash "$repo_root/scripts/bench.sh" "$E2E_LOG_DIR/benchmark-smoke" --smoke
