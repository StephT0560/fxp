#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Core auth-aware liveness probe. Never print the token or enable shell tracing.
set -euo pipefail
export LC_ALL=C
exec 3<>/dev/tcp/127.0.0.1/8080
if [[ -n "${FXP_CORE_AUTH_TOKEN:-}" ]]; then
    size=${#FXP_CORE_AUTH_TOKEN}
    [[ "$size" -le 1024 ]]
    printf -v length '\\%03o\\%03o' "$((size / 256))" "$((size % 256))"
    printf 'FXPAUTH1%b%s' "$length" "$FXP_CORE_AUTH_TOKEN" >&3
    IFS= read -r -N 1 -t 5 ack <&3
    [[ "$ack" == $'\x01' ]]
fi
