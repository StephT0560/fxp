#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Smoke the local Compose stack using the exported .env credentials.

Default: verify mTLS on all listeners and a FIX cross through gateway/core.
Use --dev only for the explicit insecure development Compose profile.
"""
import argparse
from base64 import b64encode
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--dev', action='store_true')
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
env = os.environ.copy()
credential = env['FXP_FIX_CREDENTIALS'].split(',')[0]
sender, password = credential.split(':', 1)
env['FXP_FIX_PASSWORD'] = password
certs = Path(env.get('FXP_CERTS_DIR', root / 'certs')).resolve()
context = None
if not args.dev:
    context = ssl.create_default_context(cafile=certs / 'ca.crt')
    context.load_cert_chain(certs / 'client.crt', certs / 'client.key')
    env.update(FXP_CLIENT_TLS='true', FXP_CLIENT_TLS_CA_FILE=str(certs / 'ca.crt'),
               FXP_CLIENT_TLS_CERT_FILE=str(certs / 'client.crt'),
               FXP_CLIENT_TLS_KEY_FILE=str(certs / 'client.key'),
               FXP_CLIENT_TLS_SERVER_NAME='localhost')
else:
    env['FXP_CLIENT_TLS'] = 'false'
    for key in list(env):
        if key.startswith('FXP_CLIENT_TLS_'):
            del env[key]

for port in [8081, 8082, 8083]:
    for attempt in range(50):
        try:
            raw = socket.create_connection(('127.0.0.1', port), timeout=2)
            break
        except OSError:
            if attempt == 49:
                raise
            time.sleep(0.2)
    with raw:
        conn = context.wrap_socket(raw, server_hostname='localhost') if context else raw
        with conn:
            if port == 8081:
                key = b64encode(os.urandom(16)).decode()
                request = (f'GET /ws HTTP/1.1\r\nHost: localhost:8081\r\n'
                           f'Upgrade: websocket\r\nConnection: Upgrade\r\n'
                           f'Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n')
                conn.sendall(request.encode())
                response = conn.recv(4096)
                if not response.startswith(b'HTTP/1.1 101'):
                    raise SystemExit('WebSocket upgrade failed')
    print(f'PASS {"mTLS" if context else "development plaintext"} listener :{port}')

subprocess.run([sys.executable, str(root / 'fix_test_client/fix_client.py'),
                '--sender', sender, '--test', 'limit_cross'], env=env, check=True)
print('PASS Compose FIX cross through gateway and core')
