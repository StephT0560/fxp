#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Repeated before/after slow-consumer work/retained-memory fixtures, no timings."""
import argparse
import json
import os
from phase1_lite import ROOT, run


def main():
    parser = argparse.ArgumentParser(__doc__)
    parser.add_argument('--output', default='benchmarks/subscriber-isolation')
    args = parser.parse_args()
    scratch = ROOT/'work/subscriber-isolation'
    out = ROOT/args.output
    out.mkdir(parents=True, exist_ok=True)
    values = []
    for repetition in range(1, 6):
        for revision in ['before', 'after']:
            entry = {'revision': revision, 'repetition': repetition}
            for component, binary, command in [
                ('core', 'core.test', ['--ignored', '--exact', 'server::phase1::core_slow_consumer', '--test-threads=1']),
                ('gateway', 'gateway.test', ['-test.run', '^TestPhase1SlowConsumer$'])]:
                target = scratch/f'slow-{revision}-{repetition}-{component}.json'
                run([scratch/revision/binary, *command], env=dict(os.environ, FXP_PHASE1_OUTPUT=str(target)), log=scratch/f'slow-{revision}-{repetition}-{component}.log')
                entry[component] = json.loads(target.read_text())
            values.append(entry)
    (out/'slow-counts.json').write_text(json.dumps(values, indent=2)+'\n')
    print('PASS repeated slow-consumer count and retained-memory fixtures')


if __name__ == '__main__':
    main()
