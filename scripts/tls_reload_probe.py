#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Verify that all three live listeners observe certificate-file rotation."""
import hashlib
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import time

certs = Path(sys.argv[1]).resolve()
ctx = ssl.create_default_context(cafile=str(certs / 'ca.crt'))
ctx.minimum_version = ssl.TLSVersion.TLSv1_2
ctx.load_cert_chain(str(certs / 'client.crt'), str(certs / 'client.key'))


def fingerprint(port):
    with socket.create_connection(('127.0.0.1', port), timeout=2) as raw:
        with ctx.wrap_socket(raw, server_hostname='localhost') as connection:
            return hashlib.sha256(connection.getpeercert(binary_form=True)).hexdigest()


before = {port: fingerprint(port) for port in (8081, 8082, 8083)}
subprocess.run(['bash', str(Path(__file__).resolve().parent / 'gen_test_certs.sh'),
                str(certs), '--rotate-server'], check=True)
deadline = time.monotonic() + 8
remaining = set(before)
while remaining and time.monotonic() < deadline:
    for port in list(remaining):
        if fingerprint(port) != before[port]:
            remaining.remove(port)
            print(f'PASS live TLS certificate reload on :{port}', flush=True)
    if remaining:
        time.sleep(0.1)
if remaining:
    raise SystemExit(f'certificate reload timed out on {sorted(remaining)}')
