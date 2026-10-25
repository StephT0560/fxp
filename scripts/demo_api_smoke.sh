#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
for port in 8080 8081 8082; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        printf 'error: port %s already in use\n' "$port" >&2; exit 1
    fi
done
bash scripts/gen_proto.sh
mkdir -p bin
(cd fxp_core && cargo build --release --locked)
(cd go_gateway && go build -o ../bin/go_gateway . && go build -o ../bin/simday ./simulation)
(cd demo_api && go build -o ../bin/demo_api . && go test -race -tags integration -count=1 -run '^TestIntegrationRealStack$' -v .)
