# SPDX-License-Identifier: MIT
"""Local process tests; build the core/replay and bin/go_gateway first."""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import struct
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError
from urllib.request import urlopen

from prometheus_client.parser import text_string_to_metric_families

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / 'fxp_core/target/release/fxp_core'
REPLAY = ROOT / 'fxp_core/target/release/fxp-replay'
GATEWAY = ROOT / 'bin/go_gateway'
GOLDEN = ROOT / 'fxp_core/tests/bundles/partial-fill'


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def wait_for(predicate, processes=()):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if any(p.poll() is not None for p in processes):
            raise AssertionError('local service exited before readiness')
        try:
            value = predicate()
            if value:
                return value
        except (OSError, HTTPError):
            pass
        time.sleep(.02)
    raise AssertionError('local service readiness timed out')


def scrape(endpoint):
    with urlopen(endpoint, timeout=2) as response:
        if response.headers.get_content_type() != 'text/plain':
            raise AssertionError('incorrect metrics content type')
        text = response.read(65536).decode()
    families = list(text_string_to_metric_families(text))
    return text, {(sample.name, tuple(sorted(sample.labels.items()))): sample.value
                  for family in families for sample in family.samples}


def sample(values, name, **labels):
    return values[(name, tuple(sorted(labels.items())))]


def varint(n):
    result = bytearray()
    while n >= 128:
        result.append((n & 127) | 128)
        n >>= 7
    result.append(n)
    return bytes(result)


def field(number, value):
    return varint(number * 8 + 2) + varint(len(value)) + value


def frame(connection, payload):
    connection.sendall(struct.pack('>I', len(payload)) + payload)


def receive(connection):
    def read(n):
        out = bytearray()
        while len(out) < n:
            data = connection.recv(n - len(out))
            if not data:
                raise AssertionError('unexpected disconnect')
            out.extend(data)
        return bytes(out)
    size = struct.unpack('>I', read(4))[0]
    if size > 1048576:
        raise AssertionError('oversized test response')
    return read(size)


class MetricsTests(unittest.TestCase):
    def test_both_endpoints_parse_and_observe_private_recorded_commands(self):
        for binary in (CORE, GATEWAY):
            self.assertTrue(binary.is_file(), f'build {binary.name} first')
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            core_port, gw_port, tcp, ws = port(), port(), port(), port()
            core_url = f'http://127.0.0.1:{core_port}/metrics'
            gw_url = f'http://127.0.0.1:{gw_port}/metrics'
            secret = secrets.token_hex(32)
            env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
            env.update(FXP_CORE_METRICS_ADDR=str(core_port), FXP_GW_METRICS_ADDR=f':{gw_port}',
                       FXP_GW_TCP_ADDR=f'127.0.0.1:{tcp}', FXP_GW_WS_ADDR=f'127.0.0.1:{ws}',
                       FXP_JWT_SECRET=secret, FXP_RUN_RECORD=str(work / 'bundle'), RUST_LOG='warn')
            processes, logs = [], []
            try:
                for binary in (CORE, GATEWAY):
                    log = open(work / (binary.name + '.log'), 'wb')
                    logs.append(log)
                    processes.append(subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log))
                    url = core_url if binary == CORE else gw_url
                    wait_for(lambda: scrape(url), processes)
                    data_port = 8080 if binary == CORE else tcp
                    def ready():
                        with socket.create_connection(('127.0.0.1', data_port), timeout=.2):
                            return True
                    wait_for(ready, processes)
                for url in (core_url, gw_url):
                    text, values = scrape(url)
                    self.assertEqual(sample(values, 'fxp_build_info', version='0.1.0', git_sha=self.build_sha(text)), 1)
                    self.assertGreaterEqual(sample(values, 'fxp_process_uptime_seconds'), 0)
                    with self.assertRaises(HTTPError) as error:
                        urlopen(url.replace('/metrics', '/other'), timeout=2)
                    self.assertEqual(error.exception.code, 404)
                    error.exception.close()
                private = 'private-owner-never-a-label'
                encode = lambda data: base64.urlsafe_b64encode(json.dumps(data, separators=(',', ':')).encode()).rstrip(b'=')
                token_body = encode({'alg': 'HS256'}) + b'.' + encode({'sub': private, 'iss': 'fxp-dev', 'aud': 'fxp-gateway', 'iat': int(time.time()), 'exp': int(time.time()) + 60})
                token = token_body + b'.' + base64.urlsafe_b64encode(hmac.new(secret.encode(), token_body, hashlib.sha256).digest()).rstrip(b'=')
                # Fixture contains an invalid quantity, but valid protocol enums:
                # it reaches core and generates an actual business rejection.
                trace = json.loads((ROOT / 'fxp_core/tests/bundles/reject/timeline.json').read_text())
                order = bytes.fromhex(trace['events'][0]['data']['protobuf_hex'])
                with socket.create_connection(('127.0.0.1', tcp), timeout=3) as connection:
                    connection.settimeout(3)
                    frame(connection, field(2, field(1, token)))
                    self.assertTrue(receive(connection))
                    frame(connection, field(5, order))
                    self.assertTrue(receive(connection))
                    for url in (core_url, gw_url):
                        text, values = wait_for(lambda: self.observed(url), processes)
                        self.assertNotIn(private, text)
                        self.assertNotIn(token.decode(), text)
                        self.assertNotIn('BAD', text)
                        self.assertGreaterEqual(sample(values, 'fxp_connected_clients'), 1)
                        self.assertGreaterEqual(sample(values, 'fxp_messages_total', direction='in', type='new_order'), 1)
                        self.assertGreaterEqual(sample(values, 'fxp_messages_total', direction='out', type='execution_report'), 1)
                _, core = scrape(core_url)
                _, gateway = scrape(gw_url)
                self.assertGreaterEqual(sample(core, 'fxp_commands_processed_total'), 1)
                self.assertGreaterEqual(sample(gateway, 'fxp_commands_forwarded_total'), 1)
                count = sample(core, 'fxp_journal_fsync_duration_seconds_count')
                self.assertGreaterEqual(count, 1)
                self.assertEqual(sample(core, 'fxp_journal_fsync_total'), count)
                self.assertEqual(sample(core, 'fxp_journal_fsync_duration_seconds_bucket', le='+Inf'), count)
                buckets = sorted((float(dict(labels)['le']), value) for (name, labels), value in core.items() if name == 'fxp_journal_fsync_duration_seconds_bucket')
                self.assertEqual([v for _, v in buckets], sorted(v for _, v in buckets))
                wait_for(lambda: sample(scrape(gw_url)[1], 'fxp_connected_clients') == 0, processes)
            finally:
                # Stop gateway first, then finalize recording with SIGTERM.
                for process in reversed(processes):
                    if process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            process.kill(); process.wait()
                for log in logs:
                    log.close()
            result = subprocess.run([str(REPLAY), 'verify', str(work / 'bundle')], capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertIn(b'IDENTICAL', result.stdout)

    @staticmethod
    def build_sha(text):
        values = [s for f in text_string_to_metric_families(text) for s in f.samples if s.name == 'fxp_build_info']
        return values[0].labels['git_sha']

    @staticmethod
    def observed(url):
        text, values = scrape(url)
        if sample(values, 'fxp_messages_total', direction='out', type='execution_report') < 1:
            return None
        if sum(v for (n, _), v in values.items() if n == 'fxp_rejects_total') < 1:
            return None
        return text, values


class ReplayLimitsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bundle = self.root / 'bundle'
        shutil.copytree(GOLDEN, self.bundle)

    def run_cli(self, *args):
        result = subprocess.run([str(REPLAY), *map(str, args)], capture_output=True, timeout=5)
        self.assertNotIn(b'panicked', result.stderr)
        self.assertNotIn(b'stack overflow', result.stderr)
        self.assertIn(result.returncode, (0, 1, 2, 3))
        return result

    def limit(self, result, name):
        self.assertEqual(result.returncode, 3, result.stderr.decode())
        self.assertEqual(len(result.stderr.splitlines()), 1)
        self.assertEqual(json.loads(result.stderr), {'error': 'resource_limit', 'limit': name})

    def manifest(self, change):
        path = self.bundle / 'manifest.json'
        value = json.loads(path.read_text())
        change(value)
        path.write_text(json.dumps(value))

    def test_unlimited_and_generous_limits_preserve_golden_output(self):
        plain = self.run_cli('verify', self.bundle)
        limited = self.run_cli('--max-events', 10000, 'verify', self.bundle, '--max-bytes=1000000', '--max-wall-seconds', 3, '--max-memory-mb', 256)
        self.assertEqual(plain.returncode, 0)
        self.assertEqual((plain.stdout, plain.stderr), (limited.stdout, limited.stderr))
        self.assertEqual(limited.returncode, 0)

    def test_limits_cover_all_subcommands_and_do_not_publish_reports(self):
        commands = [('verify',), ('timeline', '--json'), ('explain',), ('export', '--jsonl'), ('diff', str(GOLDEN)), ('report', '--html', str(self.root / 'report.html'))]
        for command in commands:
            with self.subTest(command=command[0]):
                args = [command[0], str(self.bundle), *command[1:], '--max-bytes', '1']
                self.limit(self.run_cli(*args), 'max-bytes')
        self.assertFalse((self.root / 'report.html').exists())

    def test_synthetic_event_stream_and_wall_memory_budgets(self):
        trace = self.bundle / 'events.jsonl'
        row = trace.read_bytes().splitlines(keepends=True)[0]
        trace.write_bytes(row * 10000)
        self.manifest(lambda m: m.update(trace_sha256=hashlib.sha256(trace.read_bytes()).hexdigest()))
        self.limit(self.run_cli('verify', self.bundle, '--max-events', 100), 'max-events')
        self.limit(self.run_cli('verify', self.bundle, '--max-wall-seconds', '.000000001'), 'max-wall-seconds')
        self.limit(self.run_cli('verify', self.bundle, '--max-memory-mb', 1), 'max-memory-mb')

    def test_bad_limit_arguments_never_panic(self):
        for args in [('--max-events', '-1'), ('--max-bytes', 'x'), ('--max-wall-seconds', 'NaN'), ('--max-wall-seconds', 'inf'), ('--max-wall-seconds', '1e300'), ('--max-memory-mb', '18446744073709551615'), ('--max-events', '1', '--max-events=2')]:
            with self.subTest(args=args):
                self.assertEqual(self.run_cli('verify', self.bundle, *args).returncode, 2)

    def test_hostile_json_strings_nesting_and_manifest_paths(self):
        self.manifest(lambda m: m.update(meta={'huge': 'x' * 2000000}))
        self.limit(self.run_cli('verify', self.bundle, '--max-bytes', 1024), 'max-bytes')
        shutil.copyfile(GOLDEN / 'manifest.json', self.bundle / 'manifest.json')
        self.manifest(lambda m: m.update(symbol_config_file='../../outside.csv'))
        outside = self.root / 'outside.csv'; outside.write_text('private sentinel')
        self.assertEqual(self.run_cli('verify', self.bundle).returncode, 2)
        self.assertEqual(outside.read_text(), 'private sentinel')
        (self.bundle / 'manifest.json').write_text('{"meta":' + '[' * 10000 + '0' + ']' * 10000 + '}')
        result = self.run_cli('verify', self.bundle, '--max-bytes', 1000000, '--max-wall-seconds', 2)
        self.assertEqual(result.returncode, 2)
        self.assertIn(b'nesting', result.stderr)

    def test_truncated_and_oversized_binary_records_fail_cleanly(self):
        for content in [b'FXPEVL01\x00', b'FXPEVL01\xff\xff\xff\xff']:
            (self.bundle / 'commands.log').write_bytes(content)
            self.manifest(lambda m: m.update(commands_sha256=hashlib.sha256(content).hexdigest()))
            self.assertEqual(self.run_cli('verify', self.bundle, '--max-wall-seconds', 2).returncode, 2)

    def test_symlinks_and_special_files_are_not_followed(self):
        command = self.bundle / 'commands.log'; command.unlink()
        command.symlink_to(GOLDEN / 'commands.log')
        self.assertEqual(self.run_cli('verify', self.bundle).returncode, 2)
        command.unlink(); os.mkfifo(command)
        self.assertEqual(self.run_cli('verify', self.bundle, '--max-wall-seconds', 2).returncode, 2)
