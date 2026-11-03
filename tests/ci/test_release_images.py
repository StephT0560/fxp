# SPDX-License-Identifier: MIT
"""Release publication must remain tag-only; both native image tests gate PRs."""
from pathlib import Path
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[2]


class ReleaseImageWorkflowTests(unittest.TestCase):
    def test_publication_is_tag_only_and_requires_both_architectures(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        triggers = workflow.get('on', workflow.get(True))  # PyYAML YAML 1.1
        self.assertEqual(triggers['push']['tags'], ['v*.*.*'])
        self.assertNotIn('pull_request', triggers)
        jobs = workflow['jobs']
        self.assertEqual(jobs['images']['if'], "github.event_name == 'push'")
        self.assertEqual(jobs['publish-images']['if'], "github.event_name == 'push'")
        self.assertEqual(jobs['publish-images']['needs'], 'images')
        self.assertIn('publish-images', jobs['draft']['needs'])
        self.assertNotIn('packages', workflow['permissions'])
        for name in ('images', 'publish-images'):
            self.assertEqual(jobs[name]['permissions']['packages'], 'write')
        self.check_native_pair(jobs['images'])

    def check_native_pair(self, job):
        self.assertEqual({(v['arch'], v['runner']) for v in job['strategy']['matrix']['include']},
                         {('amd64', 'ubuntu-24.04'), ('arm64', 'ubuntu-24.04-arm')})
        self.assertFalse(job['strategy']['fail-fast'])

    def test_pr_image_smoke_cannot_be_skipped_by_success_gate(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        self.assertIn('release-images', workflow['jobs']['ci-success']['needs'])
        job = workflow['jobs']['release-images']
        self.check_native_pair(job)
        self.assertNotIn('if', job)
        self.assertNotIn('continue-on-error', job)
        self.assertTrue(any(step.get('run') == 'make release-smoke' for step in job['steps']))
        self.assertNotIn('packages', workflow['permissions'])
