#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Real-stack blind/reveal output contract, including no shell recipe echo."""
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
env = dict(os.environ, FXP_REPRO_SEED='7')
blind = subprocess.run(['make', 'repro-blind'], cwd=ROOT, env=env,
                       capture_output=True, text=True, check=True)
lines = blind.stdout.splitlines()
assert not blind.stderr and len(lines) == 2, (blind.stdout, blind.stderr)
assert lines[0].startswith('Bundle: ') and lines[1].startswith('Symptom: ')
bundle = Path(lines[0].removeprefix('Bundle: '))
assert bundle.is_dir() and (bundle/'expected.json').exists()
seal = ROOT/'target/.repro-sealed.json'
state = json.loads(seal.read_text())
assert state['answer'] not in blind.stdout and state['bug'] not in blind.stdout
assert state['started'] > 0 and seal.stat().st_mode & 0o777 == 0o600
assert sorted(p.name for p in bundle.parent.iterdir()) == ['bundle']
reveal = subprocess.run(['make', 'repro-reveal'], cwd=ROOT, env=env,
                        capture_output=True, text=True, check=True)
assert state['answer'] in reveal.stdout and 'Elapsed since blind start:' in reveal.stdout
assert not seal.exists()
print('PASS blind stdout, sealed answer/start time, verified bundle and reveal')
