#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Verify only the UI is published and no JWT secret is exposed by demo APIs."""
import json
from pathlib import Path
import subprocess
import urllib.request

compose = ['docker', 'compose', '--env-file', '.env.demo', '-p', 'fxp-demo',
           '-f', 'docker-compose.dev.yml', '-f', 'docker-compose.demo.yml',
           '--profile', 'dev', '--profile', 'demo']
# Inspect only the non-secret port fields; never print expanded Compose config.
config = json.loads(subprocess.check_output(compose + ['config', '--format', 'json']))
ports = [(service, port) for service, cfg in config['services'].items()
         for port in cfg.get('ports', [])]
assert len(ports) == 1, 'demo must publish exactly one port'
service, port = ports[0]
assert service == 'core' and str(port['published']) == '3000' and port['host_ip'] == '127.0.0.1'
secret = next(line.split('=', 1)[1] for line in Path('.env.demo').read_text().splitlines()
              if line.startswith('FXP_JWT_SECRET='))
for path in ['/healthz', '/api/users', '/api/symbols', '/api/sim/status', '/']:
    with urllib.request.urlopen('http://127.0.0.1:3000' + path) as response:
        assert secret.encode() not in response.read(), 'secret exposed in response'
print('PASS demo: only 127.0.0.1:3000 published; private core/gateway; no secret in API/static responses')
