#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Measure each real CLI process with wait4 (Linux RSS KiB).

Generate fixtures first:
FXP_PROFILE_DIR=/tmp/replay-profile cargo test --release --lib \
  generate_replay_profile_bundles -- --ignored --nocapture
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def measure(binary, bundle, command, memory):
    with tempfile.TemporaryDirectory(prefix='fxp-profile-') as temp:
        args = [str(binary), command, str(bundle), '--max-events', '1000000',
                '--max-bytes', '134217728', '--max-wall-seconds', '25',
                '--max-memory-mb', str(memory)]
        if command == 'report':
            args += ['--html', str(Path(temp) / 'report.html')]
        started = time.monotonic()
        with open(Path(temp) / 'stdout', 'wb') as out, open(Path(temp) / 'stderr', 'wb') as err:
            child = subprocess.Popen(args, stdout=out, stderr=err)
            _, status, usage = os.wait4(child.pid, 0)
            child.returncode = os.waitstatus_to_exitcode(status)
        html = Path(temp) / 'report.html'
        return {'bundle': bundle.name, 'command': command, 'memory_mib': memory,
                'exit_code': child.returncode, 'peak_rss_kib': usage.ru_maxrss,
                'seconds': round(time.monotonic() - started, 3),
                'input_bytes': sum(p.stat().st_size for p in bundle.iterdir() if p.is_file()),
                'html_bytes': html.stat().st_size if html.exists() else 0,
                'stderr': (Path(temp) / 'stderr').read_text()[-2048:]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('fixtures', type=Path)
    parser.add_argument('--memory-mib', type=int, default=256)
    parser.add_argument('--check', action='store_true',
                        help='fail unless supported reports fit the worker artifact cap and 100k fails at the row limit')
    args = parser.parse_args()
    failures = []
    for size in (1000, 10000, 25000, 45000, 100000):
        for command in ('verify', 'report'):
            result = measure(args.binary.resolve(), args.fixtures / str(size), command, args.memory_mib)
            print(json.dumps(result), flush=True)
            if args.check:
                if command == 'report' and size == 100000:
                    passed = result['exit_code'] == 2 and '100000 timeline rows' in result['stderr']
                else:
                    passed = result['exit_code'] == 0
                    if command == 'report':
                        passed = passed and 0 < result['html_bytes'] <= (4 << 20)
                if not passed:
                    failures.append(f'{size}/{command}')
    if failures:
        raise SystemExit('Replay resource regression: ' + ', '.join(failures))


if __name__ == '__main__':
    main()
