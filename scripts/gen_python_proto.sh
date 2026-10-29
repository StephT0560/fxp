#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# Pin generator so checked-in bindings are stable and work on Python 3.10+.
if [[ "$(protoc --version)" != 'libprotoc 25.3' ]]; then
    echo 'error: protoc 25.3 required (see docs/bot-workflow.md)' >&2; exit 1
fi
protoc -I "$repo_root/fxp_core/proto" --python_out="$repo_root/sdk/python/fxp_client" "$repo_root/fxp_core/proto/fxp.proto"
