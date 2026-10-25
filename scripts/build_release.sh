#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Build an artifact only. Publishing a draft is confined to release.yml.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
arch="${1:?usage: build_release.sh amd64|arm64 output-directory}"
out="${2:?output directory required}"
case "$arch" in
    amd64) target=x86_64-unknown-linux-musl ;;
    arm64) target=aarch64-unknown-linux-musl ;;
    *) printf 'unsupported architecture %s\n' "$arch" >&2; exit 1 ;;
esac
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cd "$repo_root"
command -v syft >/dev/null || { printf 'syft v1.27.1 is required for release SBOMs\n' >&2; exit 1; }
syft version | tee "$out/syft-version.txt"
grep -Eq '^Version:[[:space:]]+1\.27\.1$' "$out/syft-version.txt" || { printf 'expected syft 1.27.1\n' >&2; exit 1; }
export SYFT_CHECK_FOR_APP_UPDATE=false
bash scripts/gen_proto.sh
host="$(rustc -vV | sed -n 's/^host: //p')"
linker="$(rustc --print sysroot)/lib/rustlib/$host/bin/rust-lld"
linker_key="CARGO_TARGET_$(tr '[:lower:]-' '[:upper:]_' <<<"$target")_LINKER"
release_ref="${GITHUB_REF_NAME:-$(git describe --always --dirty)}"
package="$out/fxp-linux-$arch"
mkdir -p "$package"
(cd fxp_core && env "$linker_key=$linker" cargo build --release --locked --target "$target" --bin fxp_core)
cp "fxp_core/target/$target/release/fxp_core" "$package/"
(cd go_gateway && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$package/go_gateway" .)
(cd fix_bridge && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$package/fix_bridge" .)
python3 scripts/verify_static_elf.py "$arch" "$package/fxp_core" "$package/go_gateway" "$package/fix_bridge"
cp LICENSE "$package/LICENSE"
{
    git rev-parse HEAD
    printf 'Source ref: %s\n' "$release_ref"
    rustc --version
    go version
    printf 'Rust target: %s; linker: bundled rust-lld\nGo CGO_ENABLED=0 GOOS=linux GOARCH=%s\n' "$target" "$arch"
    printf 'SBOM source inventory includes all locked Rust build/dev dependencies; binary SBOM identifies packaged Go dependencies.\n'
} >"$package/BUILD-INFO.txt"
# Limit the source scan to lockfiles/manifests; do not catalog generated target
# trees or download caches as if they were shipped application dependencies.
sources="$out/sbom-source-$arch"
mkdir -p "$sources/fxp_core" "$sources/go_gateway" "$sources/fix_bridge"
cp fxp_core/Cargo.toml fxp_core/Cargo.lock "$sources/fxp_core/"
cp go_gateway/go.mod go_gateway/go.sum "$sources/go_gateway/"
cp fix_bridge/go.mod fix_bridge/go.sum "$sources/fix_bridge/"
syft scan "dir:$sources" --source-name fxp-source --source-version "$release_ref" -o "cyclonedx-json=$out/fxp-linux-$arch-source.cdx.json"
syft scan "dir:$package" --source-name "fxp-linux-$arch" --source-version "$release_ref" -o "cyclonedx-json=$out/fxp-linux-$arch-binaries.cdx.json"
python3 - "$out" "$arch" <<'PY'
import json, sys
from pathlib import Path
root, arch = Path(sys.argv[1]), sys.argv[2]
for scope in ['source', 'binaries']:
    data=json.loads((root/f'fxp-linux-{arch}-{scope}.cdx.json').read_text())
    if data.get('bomFormat')!='CycloneDX' or not data.get('components'):
        raise SystemExit(f'empty or invalid {scope} SBOM')
PY
tar -C "$out" -czf "$out/fxp-linux-$arch.tar.gz" "fxp-linux-$arch"
