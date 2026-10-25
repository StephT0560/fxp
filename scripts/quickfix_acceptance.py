#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Run pinned QuickFIX C++ FIX44 definitions with its official POSIX Ruby runner."""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
PIN = '6908dc8c1084eeb7af7f322d35216e9965684489'  # QuickFIX v1.15.1


def classify(results, allowlist, runner_errors=()):
    stale = sorted(set(allowlist) - set(results))
    unexpected = sorted(k for k, ok in results.items() if not ok and k not in allowlist)
    xpass = sorted(k for k, ok in results.items() if ok and k in allowlist)
    invalid = sorted(k for k, reason in allowlist.items() if not reason.strip())
    return dict(ok=bool(results) and not (stale or unexpected or invalid or runner_errors),
                passed=sum(results.values()),
                failed=len(unexpected), allowed=sum(not ok for k, ok in results.items() if k in allowlist),
                unexpected_failures=unexpected, unexpected_passes=xpass, stale=stale, invalid=invalid, runner_errors=list(runner_errors))


def load_allowlist(config):
    name = 'expected-failures.json' if config == 'default' else 'expected-failures-quickfix.json'
    return json.loads((ROOT / 'tests/quickfix' / name).read_text())


def wait_port(port, process):
    for _ in range(200):
        if process.poll() is not None:
            raise RuntimeError(f'service exited with {process.returncode}; inspect acceptance logs')
        try:
            with socket.create_connection(('127.0.0.1', port), .1):
                return
        except OSError:
            time.sleep(.05)
    raise RuntimeError(f'port {port} never became ready')


@contextmanager
def service(command, env, log):
    with log.open('wb') as stream:
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT)
        try:
            yield process
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def prepare(source, work, password):
    if subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip() != PIN:
        raise RuntimeError('QuickFIX checkout is not at the pinned commit')
    subprocess.run(['git', '-C', str(source), 'diff', '--exit-code', 'HEAD', '--', 'test', 'LICENSE'], check=True)
    # A fresh fixture copy preserves upstream sources and intentional malformed
    # messages. The only input adaptation adds required venue Logon credentials.
    shutil.copytree(source / 'test', work, dirs_exist_ok=True)
    shutil.copy2(ROOT / 'tests/quickfix/runner_adapter.rb', work / 'fxp_runner.rb')
    for path in (work / 'definitions/server/fix44').glob('*.def'):
        lines = []
        for line in path.read_text().splitlines(keepends=True):
            # Deliberately invalid framed logons already fail before auth; do
            # not rewrite their explicit BodyLength or checksum assertions.
            if re.match(r'^I(?:\d,)?8=', line) and '\x0135=A\x01' in line and '\x019=' not in line and '\x0110=' not in line:
                line = line.rstrip('\n') + '554=' + password + '\x01\n'
            lines.append(line)
        path.write_text(''.join(lines))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--compat', choices=['default', 'quickfix'], default='default')
    parser.add_argument('--source', type=Path, default=ROOT / 'target/quickfix-source')
    parser.add_argument('--ruby', default='ruby')
    parser.add_argument('--no-build', action='store_true')
    parser.add_argument('--only', help='one definition basename (diagnosis only; no complete-suite claim)')
    parser.add_argument('--timeout', type=int, default=45)
    args = parser.parse_args()
    destination = ROOT / 'target/quickfix-acceptance' / args.compat
    destination.mkdir(parents=True, exist_ok=True)
    if not args.source.exists():
        args.source.mkdir(parents=True)
        subprocess.run(['git', 'init', str(args.source)], check=True)
        subprocess.run(['git', '-C', str(args.source), 'remote', 'add', 'origin', 'https://github.com/quickfix/quickfix.git'], check=True)
        subprocess.run(['git', '-C', str(args.source), 'fetch', '--depth=1', 'origin', PIN], check=True)
        subprocess.run(['git', '-C', str(args.source), 'checkout', '--detach', PIN], check=True)
    (ROOT / 'bin').mkdir(exist_ok=True)
    if not args.no_build:
        subprocess.run(['make', 'build'], cwd=ROOT, check=True)
        for mod in ('go_gateway', 'fix_bridge'):
            subprocess.run(['go', 'build', '-o', str(ROOT / 'bin' / mod), '.'], cwd=ROOT / mod, check=True)
    if not args.no_build:
        subprocess.run(['go', 'build', '-tags', 'quickfix_fixture', '-o', str(ROOT / 'bin/fix_bridge_fixture'), '.'], cwd=ROOT / 'fix_bridge', check=True)
    import secrets
    password = secrets.token_hex(16)
    env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
    env.update(FXP_JWT_SECRET=secrets.token_hex(32), FXP_FIX_CREDENTIALS='TW:' + password,
               FXP_FIX_STORE_DIR='', FXP_FIX_MAX_LATENCY='120s', FXP_FIX_TEST_FIXTURE='quickfix', FXP_FIX_COMPAT=args.compat)
    work = destination / 'runner'
    if work.exists():
        shutil.rmtree(work)
    prepare(args.source.resolve(), work, password)
    definitions = sorted((work / 'definitions/server/fix44').glob('*.def'))
    if args.only:
        definitions = [d for d in definitions if d.name == args.only]
    if not args.only and len(definitions) != 59:
        raise RuntimeError('pinned complete FIX44 suite must contain exactly 59 definitions')
    if not definitions:
        raise RuntimeError('no acceptance definitions selected')
    for port in (8080, 8081, 8082, 8083):
        try:
            with socket.create_connection(('127.0.0.1', port), .1):
                raise RuntimeError(f'port {port} is occupied; stop only the owned stack first')
        except OSError:
            pass
    results = {}
    runner_errors = []
    with service([str(ROOT / 'fxp_core/target/release/fxp_core')], env, destination / 'core.log') as core:
        wait_port(8080, core)
        with service([str(ROOT / 'bin/go_gateway')], env, destination / 'gateway.log') as gateway:
            wait_port(8082, gateway)
            for definition in definitions:
                with service([str(ROOT / 'bin/fix_bridge_fixture'), '-test-fixture', '-compid', 'ISLD', '-addr', '127.0.0.1:8083'], env, destination / (definition.stem + '.bridge.log')) as bridge:
                    wait_port(8083, bridge)
                    try:
                        run = subprocess.run([args.ruby, '--disable-gems', 'fxp_runner.rb', '127.0.0.1', '8083', str(definition.relative_to(work))], cwd=work, env=env, capture_output=True, text=True, timeout=args.timeout)
                        output = run.stdout + run.stderr
                        # Require the upstream runner's actual one-test outcome,
                        # not just an exit status (exceptions may exit zero).
                        match = re.search(r"<results total='1' failures='([01])'/>", output)
                        if match is None:
                            runner_errors.append(definition.name)
                        ok = run.returncode == 0 and match is not None and match[1] == '0'
                    except subprocess.TimeoutExpired as error:
                        output = str(error)
                        runner_errors.append(definition.name)
                        ok = False
                    results[definition.name] = ok
                    (destination / (definition.stem + '.runner.log')).write_text(output)
                    print(('PASS ' if ok else 'FAIL ') + definition.name, flush=True)
    allowlist = load_allowlist(args.compat)
    if args.only:
        allowlist = {k: v for k, v in allowlist.items() if k in results}
    summary = classify(results, allowlist, runner_errors)
    (destination / 'results.json').write_text(json.dumps(dict(pin=PIN, config=args.compat, fixture='quickfix', complete_suite=not args.only, results=results, summary=summary), indent=2) + '\n')
    print(json.dumps(summary, indent=2), flush=True)
    return 0 if summary['ok'] else 1


if __name__ == '__main__':
    sys.exit(main())
