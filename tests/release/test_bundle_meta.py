# SPDX-License-Identifier: MIT
"""F2 pass-through assertions used by the real Compose/SDK integration smoke."""
import copy
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_smoke', ROOT / 'scripts/release_smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class BundleMetaTests(unittest.TestCase):
    def test_bundle_and_report_must_preserve_all_image_references(self):
        sha = 'a' * 40
        meta = {'images': {name: f'ghcr.io/example/fxp/{name}@sha256:' + 'b' * 64
                           for name in smoke.COMPONENTS}, 'seed': 7}
        manifest = {'complete': True, 'git_sha': sha, 'meta': meta}
        report = {'schema': 'fxp-run-report-v1', 'manifest': {'meta': {**meta, 'seed': '7'}},
                  'verification': {'result': 'identical'}}
        smoke.check_provenance(manifest, report, meta, sha)
        for source in ('manifest', 'report'):
            changed_manifest, changed_report = copy.deepcopy(manifest), copy.deepcopy(report)
            target = changed_manifest if source == 'manifest' else changed_report['manifest']
            target['meta']['images'].pop('simday')
            with self.subTest(source=source), self.assertRaises(AssertionError):
                smoke.check_provenance(changed_manifest, changed_report, meta, sha)
        for field, value in [('complete', False), ('git_sha', 'unknown')]:
            with self.subTest(field=field), self.assertRaises(AssertionError):
                smoke.check_provenance({**manifest, field: value}, report, meta, sha)
        with self.assertRaises(AssertionError):
            smoke.check_provenance(manifest, {**report, 'verification': {'result': 'incomplete'}}, meta, sha)
