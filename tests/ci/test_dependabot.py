# SPDX-License-Identifier: MIT
from pathlib import Path
import unittest
import yaml

class DependabotTests(unittest.TestCase):
    def test_bounded_labeled_groups(self):
        config = yaml.safe_load((Path(__file__).resolve().parents[2] / '.github/dependabot.yml').read_text())
        for update in config['updates']:
            self.assertEqual(update['open-pull-requests-limit'], 5)
            self.assertIn('dependencies', update['labels'])
            group, = update['groups'].values()
            self.assertEqual(group['patterns'], ['*'])
            if update['package-ecosystem'] != 'github-actions':
                self.assertEqual(group['update-types'], ['minor', 'patch'])
