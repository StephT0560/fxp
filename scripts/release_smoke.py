#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Images-only, localhost SDK -> recorded bundle -> offline verify/report smoke.

Build/pull images and install sdk/python BEFORE running this test. This runner
never builds, pulls, contacts a registry, or prints credentials.
"""
import argparse
import base64
import json
import os
import re
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import zlib

def report_payload(html):
    """Decode the producer's versioned payload before checking provenance.

    Keep byte limits, reject duplicate keys/trailing streams/nested codecs, and
    compare the complete decoded document with the separately exported JSON.
    """
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate report field')
            result[key] = value
        return result

    marker = '<script id="report-data" type="application/json">'
    if len(html) > 100 * 1024 * 1024 or html.count(marker) != 1:
        raise ValueError('report HTML limit or duplicate payload')
    matches = re.findall(re.escape(marker) + r'([^<]*)</script>', html)
    if len(matches) != 1:
        raise ValueError('invalid report payload')
    payload = json.loads(matches[0], object_pairs_hook=unique)
    if isinstance(payload, dict) and 'encoding' in payload:
        if set(payload) != {'encoding', 'decoded_bytes', 'data'} or payload['encoding'] != 'zlib-base64-v1':
            raise ValueError('unknown report encoding')
        size = payload['decoded_bytes']
        if not isinstance(size, str) or not size.isascii() or not size.isdecimal() or len(size) > 9:
            raise ValueError('invalid report byte count')
        count = int(size)
        if str(count) != size or count > 96 * 1024 * 1024:
            raise ValueError('report data limit')
        compressed = base64.b64decode(payload['data'], validate=True)
        decoder = zlib.decompressobj()
        raw = decoder.decompress(compressed, count + 1)
        if len(raw) != count or not decoder.eof or decoder.unused_data or decoder.unconsumed_tail:
            raise ValueError('truncated, trailing or oversized report data')
        payload = json.loads(raw.decode('utf-8'), object_pairs_hook=unique)
    if not isinstance(payload, dict) or 'encoding' in payload:
        raise ValueError('invalid or nested report payload')
    return payload


ROOT = Path(__file__).resolve().parents[1]
COMPONENTS = ('fxp_core', 'go_gateway', 'simday', 'fix_bridge', 'fxp-replay')


def report_numbers(value):
    # fxp-run-report-v1 preserves integers as decimal strings, including meta.
    if isinstance(value, bool):
        return value
    if isinstance(value, int):
        return str(value)
    if isinstance(value, dict):
        return {key: report_numbers(item) for key, item in value.items()}
    if isinstance(value, list):
        return [report_numbers(item) for item in value]
    return value


def check_provenance(manifest, report, expected_meta, revision):
    if not manifest.get('complete'):
        raise AssertionError('recording is not complete')
    if manifest.get('git_sha') != revision:
        raise AssertionError('build SHA not preserved')
    if manifest.get('meta') != expected_meta:
        raise AssertionError('bundle metadata was not preserved exactly')
    if report.get('schema') != 'fxp-run-report-v1':
        raise AssertionError('unexpected report schema')
    if report.get('verification', {}).get('result') != 'identical':
        raise AssertionError('report is not verified')
    if report.get('manifest', {}).get('meta') != report_numbers(expected_meta):
        raise AssertionError('report metadata was not preserved exactly')


def main():
    from fxp_client import Client, dev_token, pb

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image-prefix', default=os.getenv('FXP_IMAGE_PREFIX', 'fxp-release'))
    parser.add_argument('--image-tag', default=os.getenv('FXP_IMAGE_TAG', 'dev'))
    parser.add_argument('--metadata-dir', type=Path, default=ROOT / 'target/release-images')
    args = parser.parse_args()
    work = Path(tempfile.mkdtemp(prefix='release-smoke.', dir=ROOT / 'target'))
    project = 'fxp-release-' + secrets.token_hex(6)
    meta = {'images': {}, 'seed': 7}
    env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
    env.update(FXP_IMAGE_PREFIX=args.image_prefix, FXP_IMAGE_TAG=args.image_tag,
               FXP_JWT_SECRET=secrets.token_hex(32), FXP_CORE_AUTH_TOKEN=secrets.token_hex(32),
               FXP_RUN_META_JSON=json.dumps(meta), FXP_RUN_ID='smoke', FXP_SIM_DURATION='5',
               FXP_TCP_PORT='0', FXP_WS_PORT='0', FXP_FIX_PORT='0')
    compose = ['docker', 'compose', '--env-file', '/dev/null', '-p', project,
               '-f', str(ROOT / 'deploy/compose.release.yml')]

    def run(command, **kwargs):
        return subprocess.run(command, env=env, cwd=ROOT, check=True, text=True,
                              capture_output=True, timeout=180, **kwargs).stdout.strip()

    def dc(*command):
        return run(compose + list(command))

    revision = None
    for component in COMPONENTS:
        image = f'{args.image_prefix}/{component}:{args.image_tag}'
        info, = json.loads(run(['docker', 'image', 'inspect', image]))
        config = info['Config']
        if config['User'] != '65532:65532':
            raise AssertionError(f'{component}: expected non-root UID/GID')
        labels = config.get('Labels', {})
        if labels.get('org.opencontainers.image.version') != args.image_tag:
            raise AssertionError(f'{component}: version label mismatch')
        sha = labels.get('org.opencontainers.image.revision', '')
        if len(sha) != 40 or any(c not in '0123456789abcdef' for c in sha):
            raise AssertionError(f'{component}: missing full build revision')
        if revision is not None and sha != revision:
            raise AssertionError('images do not share one revision')
        revision = sha
        repository = f'{args.image_prefix}/{component}'
        refs = [value for value in info.get('RepoDigests', [])
                if value.startswith(repository + '@sha256:')]
        if refs:
            reference = refs[0]
        else:
            # --load does not populate RepoDigests. Use the BuildKit export's
            # actual manifest digest, correlated to the loaded config ID.
            artifact = json.loads((args.metadata_dir / f'{component}.json').read_text())
            if artifact.get('containerimage.config.digest') != info['Id']:
                raise AssertionError(f'{component}: stale build metadata')
            reference = repository + '@' + artifact['containerimage.digest']
        if not re.fullmatch(re.escape(repository) + r'@sha256:[0-9a-f]{64}', reference):
            raise AssertionError(f'{component}: invalid manifest digest reference')
        meta['images'][component] = reference
    env['FXP_RUN_META_JSON'] = json.dumps(meta)
    print('PASS five non-root images with version and common Git revision labels', flush=True)
    report_container = project + '-report'
    try:
        # No build or pull is allowed at test time.
        dc('up', '-d', '--no-build', '--pull', 'never', '--wait', 'core', 'gateway', 'simday')
        endpoint = dc('port', 'core', '8082')
        port = int(endpoint.rsplit(':', 1)[1])

        def connect(owner):
            token = dev_token(owner, secret=env['FXP_JWT_SECRET'],
                              issuer='fxp-release', audience='fxp-release-gateway')
            return Client.connect('127.0.0.1', port, token, timeout=5)

        def wait(client, order_id, kind):
            for _ in range(100):
                event = client.recv()
                if event.kind == 'execution_report':
                    value = event.message.execution_report
                    if value.order_id == order_id and value.execution_type == kind:
                        return value
                if event.kind == 'error_message':
                    raise AssertionError('gateway returned an error')
            raise AssertionError('missing expected execution report')

        with connect('release-maker') as maker, connect('release-taker') as taker:
            maker.new('ask', 'SDKTEST', pb.SELL, 10, 10000)
            wait(maker, 'ask', pb.EXEC_NEW)
            taker.new('buy', 'SDKTEST', pb.BUY, 4, 10000)
            fill = wait(taker, 'buy', pb.EXEC_FILLED)
            if fill.executed_quantity != 4:
                raise AssertionError('unexpected fill quantity')
        print('PASS Python SDK authenticated order acknowledgment and fill', flush=True)
        sim_id = dc('ps', '-a', '-q', 'simday')
        deadline = time.monotonic() + 30
        while True:
            state = json.loads(run(['docker', 'inspect', sim_id]))[0]['State']
            if not state['Running']:
                if state['ExitCode'] != 0:
                    raise AssertionError('simday failed')
                break
            if time.monotonic() >= deadline:
                raise AssertionError('simday did not complete')
            time.sleep(.1)
        sim_log = dc('logs', '--no-color', 'simday')
        if 'All participants connected' not in sim_log:
            raise AssertionError('simday never authenticated its participants')
        print('PASS simday completed its six-participant run', flush=True)
        # Draining core while producers still run is not a safe finalize protocol.
        dc('stop', 'gateway')
        dc('stop', 'core')
        for service in ('core', 'gateway'):
            ident = dc('ps', '-a', '-q', service)
            state = json.loads(run(['docker', 'inspect', ident]))[0]['State']
            if state['Running'] or state['ExitCode'] != 0:
                raise AssertionError(f'{service}: unsuccessful SIGTERM drain')
        core_id = dc('ps', '-a', '-q', 'core')
        run(['docker', 'cp', f'{core_id}:/data/smoke/manifest.json', str(work / 'manifest.json')])
        manifest = json.loads((work / 'manifest.json').read_text())
        output = dc('run', '--rm', '--no-deps', '--pull', 'never', 'replay', 'verify', '/runs/smoke')
        if not output.startswith('IDENTICAL events='):
            raise AssertionError('verify did not return IDENTICAL')
        print(output, flush=True)
        # Explicitly named stopped container permits safe docker cp before rm.
        dc('run', '--name', report_container, '--no-deps', '--pull', 'never', 'replay',
           'report', '/runs/smoke', '--html', '/data/report.html', '--json', '/data/report.json')
        run(['docker', 'cp', f'{report_container}:/data/report.json', str(work / 'report.json')])
        run(['docker', 'cp', f'{report_container}:/data/report.html', str(work / 'report.html')])
        report = json.loads((work / 'report.json').read_text())
        check_provenance(manifest, report, meta, revision)
        html = (work / 'report.html').read_text()
        html_report = report_payload(html)
        if html_report != report:
            raise AssertionError('HTML and JSON reports differ')
        check_provenance(manifest, html_report, meta, revision)
        print('PASS exact FXP_RUN_META_JSON image references in complete bundle and verified report', flush=True)
    except subprocess.CalledProcessError as error:
        # Subprocess arguments contain no secrets. Do not dump expanded Compose
        # config, environment, container inspect output, or service logs here.
        raise RuntimeError(f'local Docker command failed (exit {error.returncode}); inspect {project}') from None
    finally:
        subprocess.run(['docker', 'rm', '-f', report_container], env=env,
                       capture_output=True, timeout=30)
        subprocess.run(compose + ['down', '--volumes', '--remove-orphans'], env=env,
                       capture_output=True, timeout=60)
    print(f'PASS images-only release smoke; non-secret artifacts: {work}', flush=True)


if __name__ == '__main__':
    (ROOT / 'target').mkdir(exist_ok=True)
    main()
