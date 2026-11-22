# SPDX-License-Identifier: MIT
import io
import json
import ssl
import unittest
import urllib.error
from unittest.mock import Mock, patch

from fxp_client import sandbox

RUN = 'a' * 32
KEY = 'synthetic-api-key-0123456789abcdef'


class FakePlane:
    def __init__(self, states, fail_token=None):
        self.states, self.calls, self.fail_token = list(states), [], fail_token

    def __call__(self, request, timeout, context):
        self.calls.append((request.get_method(), request.full_url, request.headers.get('Authorization'), request.data))
        if request.full_url.endswith('/token'):
            if self.fail_token:
                raise urllib.error.HTTPError(request.full_url, self.fail_token, 'x', {}, io.BytesIO(b'{"error":"run_not_ready"}'))
            return io.BytesIO(json.dumps({'token': 'tok-%d' % len(self.calls)}).encode())
        state = self.states.pop(0) if len(self.states) > 1 else self.states[0]
        conn = {'host': '127.0.0.1', 'port': 9443, 'server_name': 'run.fxp.localhost'} if state == 'running' else None
        return io.BytesIO(json.dumps({'id': RUN, 'state': state, 'connection': conn}).encode())


class SandboxConnect(unittest.TestCase):
    def setUp(self):
        self.tls = object()
        p = patch('ssl.create_default_context', return_value=self.tls)
        p.start()
        self.addCleanup(p.stop)
        s = patch('time.sleep')
        s.start()
        self.addCleanup(s.stop)

    def connect(self, plane, effects, **env):
        client = Mock()
        with patch('urllib.request.urlopen', plane), patch.object(sandbox.Client, 'connect', side_effect=[*effects, client]) as connect, \
                patch.dict('os.environ', {'FXP_API_KEY': KEY, 'FXP_RUN_ID': RUN, **env}, clear=True):
            result = sandbox.connect(bot='alpha')
        return result, client, connect

    def test_waits_then_mints_a_fresh_token_per_logon_attempt(self):
        plane = FakePlane(['requested', 'starting', 'running'])
        result, client, connect = self.connect(plane, [PermissionError('token expired')])
        self.assertIs(result, client)
        self.assertEqual([c[0] for c in plane.calls], ['GET', 'GET', 'GET', 'POST', 'POST'])
        self.assertTrue(all(c[1].startswith('https://localhost:8443/v1/runs/' + RUN) and c[2] == 'Bearer ' + KEY for c in plane.calls))
        self.assertEqual(json.loads(plane.calls[3][3]), {'bot_name': 'alpha'})
        self.assertEqual([c.args for c in connect.call_args_list], [('127.0.0.1', 9443, 'tok-4'), ('127.0.0.1', 9443, 'tok-5')])
        self.assertTrue(all(c.kwargs['tls'] is self.tls and c.kwargs['server_hostname'] == 'run.fxp.localhost' for c in connect.call_args_list))

    def test_certificate_failures_are_never_retried(self):
        plane = FakePlane(['running'])
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.connect(plane, [ssl.SSLCertVerificationError('bad')])
        self.assertEqual([c[0] for c in plane.calls], ['GET', 'POST'])

    def test_finished_runs_and_errors_never_reveal_the_key(self):
        with self.assertRaisesRegex(RuntimeError, 'recorded'):
            self.connect(FakePlane(['recorded']), [])
        with self.assertRaises(sandbox.SandboxError) as caught:
            self.connect(FakePlane(['running'], fail_token=409), [])
        self.assertEqual((caught.exception.status, caught.exception.code), (409, 'run_not_ready'))
        self.assertNotIn(KEY, str(caught.exception) + repr(caught.exception))
        with patch.dict('os.environ', {'FXP_API_KEY': KEY}, clear=True):
            self.assertNotIn(KEY, repr(sandbox.Sandbox()))

    def test_requires_https_and_a_key(self):
        with patch.dict('os.environ', {}, clear=True):
            with self.assertRaises(ValueError):
                sandbox.Sandbox()
            with self.assertRaises(ValueError):
                sandbox.Sandbox('http://localhost:8443', KEY)

    def test_create_run_and_stop_use_the_documented_endpoints(self):
        calls = []

        def urlopen(request, timeout, context):
            calls.append((request.get_method(), request.full_url, json.loads(request.data) if request.data else None))
            return io.BytesIO(b'{"id":"' + RUN.encode() + b'","state":"requested"}')
        with patch('urllib.request.urlopen', urlopen):
            s = sandbox.Sandbox('https://sandbox.example/', KEY)
            self.assertEqual(s.create_run(seed=9, duration_seconds=120)['id'], RUN)
            s.stop(RUN)
        self.assertEqual(calls, [('POST', 'https://sandbox.example/v1/runs', {'template_id': 'quiet-book', 'seed': 9, 'duration_seconds': 120}),
                                 ('POST', f'https://sandbox.example/v1/runs/{RUN}/stop', None)])


if __name__ == '__main__':
    unittest.main()
