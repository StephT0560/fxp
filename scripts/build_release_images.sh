#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Local build only: never pushes images or creates a tag/release.
set -euo pipefail
cd "$(dirname "$0")/.."
args=(--file deploy/Dockerfile.release --build-arg "VERSION=${FXP_IMAGE_TAG:-dev}" --build-arg "GIT_SHA=$(git rev-parse HEAD)")
if [[ -n "${FXP_BUILD_CA:-}" ]]; then args+=(--secret "id=proxy_ca,src=$FXP_BUILD_CA"); fi
mkdir -p target/release-images
for component in fxp_core go_gateway simday fix_bridge fxp-replay; do
  docker buildx build --load --provenance=false --sbom=false "${args[@]}" --metadata-file "target/release-images/$component.json" --target "$component" -t "${FXP_IMAGE_PREFIX:-fxp-release}/$component:${FXP_IMAGE_TAG:-dev}" .
done
