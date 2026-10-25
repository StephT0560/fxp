# SPDX-License-Identifier: MIT
"""Run with python3 -m unittest discover -s tests/ci -v (requires PyYAML)."""
import json
from pathlib import Path
import subprocess
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[2]

class GateTests(unittest.TestCase):
    def test_all_jobs_and_matrix_are_required(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        jobs = workflow['jobs']
        gate = jobs['ci-success']
        self.assertEqual(set(gate['needs']), set(jobs) - {'ci-success'})
        self.assertEqual(gate['if'], 'always()')
        self.assertEqual(gate['name'], 'ci-success')
        self.assertFalse(jobs['go']['strategy']['fail-fast'])
        self.assertNotIn('continue-on-error', jobs['go'])

    def test_only_all_success_passes(self):
        jobs = ['rust', 'cargo-deny', 'go', 'e2e', 'quickfix-acceptance', 'proto', 'demo-api']
        script = ROOT / 'scripts/ci_success.py'
        def check(results):
            return subprocess.run(['python3', str(script)], input=json.dumps(results),
                                  text=True, capture_output=True).returncode
        good = {job: {'result': 'success'} for job in jobs}
        self.assertEqual(check(good), 0)
        for job in jobs:
            for result in ['failure', 'cancelled', 'skipped', 'unknown', None]:
                with self.subTest(job=job, result=result):
                    self.assertNotEqual(check({**good, job: {'result': result}}), 0)
        self.assertNotEqual(check({}), 0)
        self.assertNotEqual(check([]), 0)
