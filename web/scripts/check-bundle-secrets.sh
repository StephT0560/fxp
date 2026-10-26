#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Fails if the built console bundle contains secret env names or values.
set -euo pipefail
dist="${1:-$(dirname "$0")/../dist}"
[ -d "$dist" ] || { echo "no bundle at $dist" >&2; exit 1; }
names='FXP_JWT_SECRET|FXP_JWT_PREVIOUS_SECRET|FXP_CORE_AUTH_TOKEN|BEGIN [A-Z ]*PRIVATE KEY'
if grep -rEl "$names" "$dist"; then
  echo "secret material names found in bundle" >&2
  exit 1
fi
found=0
for var in FXP_JWT_SECRET FXP_JWT_PREVIOUS_SECRET FXP_CORE_AUTH_TOKEN; do
  val="${!var:-}"
  if [ -n "$val" ] && grep -rlF -- "$val" "$dist"; then
    echo "value of $var found in bundle" >&2
    found=1
  fi
done
[ "$found" = 0 ] && echo "bundle secret scan: clean"
