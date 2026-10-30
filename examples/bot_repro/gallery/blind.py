# SPDX-License-Identifier: MIT
"""Local exercise wrapper. The seal prevents accidental spoilers, not a security boundary."""
import argparse
import json
import os
from pathlib import Path
import random
import subprocess
import tempfile
import time

BUGS = ('stale-replace', 'stp-surprise', 'fill-cancel-race', 'auction-phase')
ROOT = Path(__file__).resolve().parents[3]


def pick(seed):
    return random.Random(seed).choice(BUGS)


def run_trial(bug, seed, folder):
    env = dict(os.environ, BUG=bug, FXP_REPRO_SEED='7', FXP_REPRO_DIR=str(folder/'run'))
    with (folder/'trial.log').open('wb') as log:
        subprocess.run(['bash', 'scripts/repro_demo.sh'], cwd=ROOT, env=env,
                       stdout=log, stderr=subprocess.STDOUT, check=True)
    run = folder/'run'
    # Move the bundle into a neutral directory; identifiers/metadata are also neutral.
    (run/'bundle').rename(folder/'bundle')
    symptom = json.loads((run/'scenario.json').read_text())['symptom']
    answer = (run/'diagnosis.txt').read_text().strip()
    # Diagnostic/source labels stay out of the exercise directory after sealing.
    import shutil
    shutil.rmtree(run)
    (folder/'trial.log').unlink()
    return answer, symptom


def blind(seed, root=ROOT/'target', seal=ROOT/'target/.repro-sealed.json', *, runner=run_trial, clock=time.time):
    root.mkdir(parents=True, exist_ok=True)
    seal.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation protects an unrevealed exercise, including concurrent starts.
    fd = os.open(seal, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    started = clock()
    try:
        folder = Path(tempfile.mkdtemp(prefix='trial-', dir=root))
        bug = pick(seed)
        answer, symptom = runner(bug, seed, folder)
        state = dict(bug=bug, seed=seed, scenario_seed=7, answer=answer, started=started,
                     symptom=symptom, bundle=str(folder/'bundle'))
        with os.fdopen(fd, 'w') as out:
            fd = None
            json.dump(state, out)
            out.write('\n')
        # Keep only the verified bundle in the visible exercise directory.
        for name in ('scenario.json', 'diagnosis.txt'):
            (folder/name).unlink(missing_ok=True)
    except BaseException:
        if fd is not None:
            os.close(fd)
        seal.unlink(missing_ok=True)
        raise
    print(f'Bundle: {folder / "bundle"}')
    print(f'Symptom: {symptom}')


def reveal(seal=ROOT/'target/.repro-sealed.json', *, clock=time.time):
    state = json.loads(seal.read_text())
    elapsed = max(0, clock() - state['started'])
    print(f"Answer ({state['bug']}): {state['answer']}")
    print(f'Elapsed since blind start: {elapsed:.1f} seconds (includes build/run time).')
    seal.unlink()  # permit the next exercise; bundle remains available


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('mode', choices=('blind', 'reveal'))
    p.add_argument('--seed', type=int, default=int(os.environ.get('FXP_REPRO_SEED', '7')))
    args = p.parse_args()
    try:
        blind(args.seed) if args.mode == 'blind' else reveal()
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        # Do not print child command/BUG labels in blind mode, including failures.
        p.exit(2, 'Exercise failed; check ports/tools, or reveal the existing exercise before starting another.\n')
