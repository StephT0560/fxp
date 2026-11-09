#!/bin/sh
# SPDX-License-Identifier: MIT
# One pinned, checksum-verified compiler installation for CI and release images.
set -eu
protoc_destination=${1:?usage: install-protoc.sh DESTINATION}
case "${TARGETARCH:-$(uname -m)}" in
  amd64|x86_64) arch=x86_64; checksum=f853e691868d0557425ea290bf7ba6384eef2fa9b04c323afab49a770ba9da80 ;;
  arm64|aarch64) arch=aarch_64; checksum=9eae1f20f70cccc912d1c318c3929b86aebf5afd4b0f32c196ef682c222ed5ae ;;
  *) echo 'unsupported architecture' >&2; exit 1 ;;
esac
protoc_temporary=$(mktemp -d)
trap 'rm -rf "$protoc_temporary"' EXIT HUP INT TERM
# The explicit download route avoids stale/failing browser responses. Hashes
# remain authoritative; a corrupt response never reaches extraction.
curl -fsSL --connect-timeout 10 --max-time 60 --retry 3 --retry-delay 1 --retry-max-time 120 \
  -o "$protoc_temporary/protoc.zip" \
  "https://github.com/protocolbuffers/protobuf/releases/download/v25.3/protoc-25.3-linux-${arch}.zip?download=1"
printf '%s  %s\n' "$checksum" "$protoc_temporary/protoc.zip" | sha256sum -c -
mkdir -p "$protoc_destination"
unzip -q -o "$protoc_temporary/protoc.zip" -d "$protoc_destination"
