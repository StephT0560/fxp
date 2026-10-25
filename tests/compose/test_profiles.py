# SPDX-License-Identifier: MIT
"""Configuration tests; no Docker daemon or private secrets needed."""
import json
import os
from pathlib import Path
import subprocess
import unittest
ROOT = Path(__file__).resolve().parents[2]

class ComposeTests(unittest.TestCase):
    def config(self, filename, token):
        env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
        env.update(FXP_JWT_SECRET='x' * 32, FXP_FIX_CREDENTIALS='TEST:fixture-password')
        if token is not None:
            env['FXP_CORE_AUTH_TOKEN'] = token
        return subprocess.run(['docker', 'compose', '--env-file', '/dev/null', '-f', filename,
                               '--profile', 'dev', 'config', '--format', 'json'], cwd=ROOT,
                              env=env, capture_output=True, text=True)

    def test_secure_requires_token(self):
        self.assertNotEqual(self.config('docker-compose.yml', None).returncode, 0)
        self.assertNotEqual(self.config('docker-compose.yml', '').returncode, 0)

    def test_secure_defaults(self):
        result = self.config('docker-compose.yml', 'y' * 32)
        self.assertEqual(result.returncode, 0, result.stderr)
        services = json.loads(result.stdout)['services']
        for name in ['gateway', 'bridge']:
            env = services[name]['environment']
            self.assertEqual(env['FXP_TLS_REQUIRED'], 'true')
            self.assertEqual(env['FXP_ALLOW_PLAINTEXT'], 'false')
            self.assertTrue(env['FXP_TLS_CERT_FILE'])
        self.assertEqual(services['bridge']['environment']['FXP_GATEWAY_TLS'], 'true')

    def test_dev_is_explicit_and_needs_no_token(self):
        result = self.config('docker-compose.dev.yml', None)
        self.assertEqual(result.returncode, 0, result.stderr)
        services = json.loads(result.stdout)['services']
        for name in ['core', 'gateway', 'bridge']:
            self.assertEqual(services[name]['profiles'], ['dev'])
        for name in ['core', 'gateway']:
            self.assertEqual(services[name]['environment']['FXP_CORE_AUTH_TOKEN'], '')
        for name in ['gateway', 'bridge']:
            self.assertEqual(services[name]['environment']['FXP_TLS_REQUIRED'], 'false')
            self.assertEqual(services[name]['environment']['FXP_ALLOW_PLAINTEXT'], 'true')
        self.assertIn('INSECURE', str(services['gateway']['command']))
