#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Export only current Criterion estimates; retain raw JSON alongside the table."""
import json
from pathlib import Path
import sys


def collect(root):
    rows = []
    for path in sorted(root.glob('**/new/estimates.json')):
        benchmark = json.loads((path.parent / 'benchmark.json').read_text())
        estimate = json.loads(path.read_text())
        rows.append({'id': benchmark['full_id'], 'estimates_ns': estimate})
    if not rows:
        raise ValueError('no Criterion estimates found')
    return {'schema_version': 1, 'unit': 'ns', 'benchmarks': rows}


if __name__ == '__main__':
    Path(sys.argv[2]).write_text(json.dumps(collect(Path(sys.argv[1])), indent=2) + '\n')
