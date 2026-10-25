# SPDX-License-Identifier: MIT
"""Process checks after E2E builds binaries; requires unused local :8080."""
import os
from pathlib import Path
import select
import socket
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]

class StartupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(dir=ROOT / 'target')
        cls.certs = Path(cls.temp.name) / 'certs'
        subprocess.run(['bash', 'scripts/gen_test_certs.sh', str(cls.certs)],
                       cwd=ROOT, check=True, capture_output=True)
        cls.env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
        cls.env.update(FXP_JWT_SECRET='x' * 32, FXP_FIX_CREDENTIALS='CHECK:fixture-password',
                       FXP_FIX_STORE_DIR=cls.temp.name + '/store')
        cls.core = subprocess.Popen([str(ROOT / 'fxp_core/target/release/fxp_core')],
                                    env=cls.env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.addClassCleanup(cls.temp.cleanup)
        cls.addClassCleanup(cls.stop, cls.core)
        for _ in range(50):
            if cls.core.poll() is not None:
                raise RuntimeError('test core failed to start (is :8080 occupied?)')
            try:
                with socket.create_connection(('127.0.0.1', 8080), timeout=.2):
                    return
            except OSError:
                time.sleep(.1)
        raise RuntimeError('test core did not become ready')

    @staticmethod
    def stop(process):
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()

    def test_both_processes_enforce_policy(self):
        for component in ['go_gateway', 'fix_bridge']:
            for label, host, tls, allow, secret, denied in [
                ('external plaintext', '0.0.0.0', False, False, 'x'*32, True),
                ('explicit override', '0.0.0.0', False, True, 'x'*32, False),
                ('loopback', '127.0.0.1', False, False, 'x'*32, False),
                ('external TLS', '0.0.0.0', True, False, 'x'*32, False),
                ('default despite override', '0.0.0.0', False, True, 'dev-secret-change-in-production', True),
                ('default despite TLS', '0.0.0.0', True, False, 'dev-secret-change-in-production', True),
                ('short JWT', '127.0.0.1', False, False, 'short', True),
            ]:
                with self.subTest(component=component, case=label):
                    env = dict(self.env, FXP_JWT_SECRET=secret,
                               FXP_ALLOW_PLAINTEXT=str(allow).lower(),
                               FXP_GW_TCP_ADDR=host+':0', FXP_GW_WS_ADDR=host+':0', FXP_FIX_ADDR=host+':0')
                    if tls:
                        env.update(FXP_TLS_CERT_FILE=str(self.certs/'server.crt'),
                                   FXP_TLS_KEY_FILE=str(self.certs/'server.key'))
                    process = subprocess.Popen([str(ROOT/'bin'/component)], env=env,
                                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                    try:
                        output, ready = b'', False
                        deadline = time.monotonic() + 10
                        while time.monotonic() < deadline:
                            if select.select([process.stdout], [], [], .1)[0]:
                                chunk = os.read(process.stdout.fileno(), 65536)
                                output += chunk
                                if not chunk:
                                    break
                            ready = (b'TCP server listening' in output and b'WebSocket server listening' in output
                                     if component == 'go_gateway' else b'FIX Bridge listening' in output)
                            if ready:
                                break
                        if denied:
                            self.assertNotEqual(process.wait(timeout=5), 0)
                            self.assertTrue(b'refusing plaintext' in output or b'security configuration' in output,
                                            'startup failed for the wrong reason')
                        else:
                            self.assertIsNone(process.poll(), 'process failed to start')
                            self.assertTrue(ready, 'listeners did not start')
                            if allow:
                                self.assertIn(b'INSECURE', output)
                    finally:
                        self.stop(process)
                        process.stdout.close()
