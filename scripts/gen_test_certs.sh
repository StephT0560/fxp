#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Disposable CA/server/client fixtures only; never use these keys in production.
set -euo pipefail
umask 077
out="${1:-target/test-certs}"
mode="${2:-create}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
if [[ "$mode" != create && "$mode" != --rotate-server ]]; then
    printf 'usage: %s [directory] [--rotate-server]\n' "$0" >&2; exit 1
fi
if [[ "$mode" == create ]]; then
    if [[ -e "$out/ca.key" ]]; then printf 'refusing to overwrite existing CA in %s\n' "$out" >&2; exit 1; fi
    openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$out/ca.key"
    openssl req -new -x509 -key "$out/ca.key" -out "$out/ca.crt" -days 2 \
        -subj '/CN=FXP throwaway test CA' \
        -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign'
    openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$out/client.key"
    openssl req -new -key "$out/client.key" -out "$out/client.csr" -subj '/CN=FXP test client'
    printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n' >"$out/client.ext"
    openssl x509 -req -in "$out/client.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" -CAcreateserial \
        -days 2 -extfile "$out/client.ext" -out "$out/client.crt"
fi
[[ -f "$out/ca.key" && -f "$out/ca.crt" ]]
rotation="$(mktemp -d "$out/.rotation.XXXXXX")"
trap 'rm -rf "$rotation"' EXIT
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$rotation/server.key"
openssl req -new -key "$rotation/server.key" -out "$rotation/server.csr" -subj '/CN=localhost'
printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1\n' >"$rotation/server.ext"
openssl x509 -req -in "$rotation/server.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" -CAcreateserial \
    -days 2 -extfile "$rotation/server.ext" -out "$rotation/server.crt"
mv "$rotation/server.key" "$out/server.key"
mv "$rotation/server.crt" "$out/server.crt"
printf 'Disposable certificates ready in %s\n' "$out"
