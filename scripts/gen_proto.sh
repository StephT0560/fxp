#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

if ! command -v protoc >/dev/null 2>&1; then
    printf 'error: protoc is required to generate FXP bindings\n' >&2
    exit 1
fi

go_cmd="$(command -v go || true)"
if [[ -z "$go_cmd" && -x /usr/local/go/bin/go ]]; then
    go_cmd=/usr/local/go/bin/go
fi
if [[ -z "$go_cmd" ]]; then
    printf 'error: Go is required to install protoc-gen-go v1.36.12\n' >&2
    exit 1
fi

gobin="${GOBIN:-$("$go_cmd" env GOPATH)/bin}"
plugin="$gobin/protoc-gen-go"
if [[ ! -x "$plugin" ]] || [[ "$("$plugin" --version 2>/dev/null || true)" != "protoc-gen-go v1.36.12" ]]; then
    mkdir -p "$gobin"
    GOBIN="$gobin" "$go_cmd" install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
fi
export PATH="$gobin:$PATH"

cd "$repo_root/fxp_core/proto"
mkdir -p ../../go_gateway/protobuf
protoc --go_out=../../go_gateway/protobuf --go_opt=paths=source_relative fxp.proto
