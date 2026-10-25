#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
case "${1:-}" in up|down) ;; *) printf 'usage: %s up|down\n' "$0" >&2; exit 1;; esac
if [[ "$1" == up ]]; then
    python3 - <<'PY'
import os, secrets, stat
path = '.env.demo'
try:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
except FileExistsError:
    if os.path.islink(path) or not stat.S_ISREG(os.stat(path).st_mode) or os.stat(path).st_mode & 0o077:
        raise SystemExit('.env.demo must be a regular private file (mode 600)')
else:
    with os.fdopen(descriptor, 'w') as output:
        output.write('FXP_JWT_SECRET=' + secrets.token_hex(32) + '\n')
        # Required by the base file's interpolation even while FIX is disabled.
        output.write('FXP_FIX_CREDENTIALS=DEMO:' + secrets.token_hex(24) + '\n')
PY
fi
# Pin a separate project and use its private env file rather than a developer's
# shell credentials. No secret values are expanded into process arguments.
unset FXP_JWT_SECRET FXP_FIX_CREDENTIALS
compose=(docker compose --env-file .env.demo -p fxp-demo -f docker-compose.dev.yml -f docker-compose.demo.yml --profile dev --profile demo)
if [[ "$1" == up ]]; then
    "${compose[@]}" up --build --detach --wait --wait-timeout 120
    printf '\nFXP Console: http://127.0.0.1:3000\n'
else
    "${compose[@]}" down --remove-orphans
fi
