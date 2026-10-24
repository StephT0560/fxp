# SPDX-License-Identifier: MIT
import importlib.util
from pathlib import Path
import unittest
spec = importlib.util.spec_from_file_location('acceptance', Path(__file__).parents[2] / 'scripts/quickfix_acceptance.py')
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)
class GateTest(unittest.TestCase):
    def test_regression_fails(self):
        self.assertFalse(acceptance.classify({'a': False}, {})['ok'])
    def test_expected_failure_and_pass_are_distinct(self):
        result = acceptance.classify({'a': True, 'b': False}, {'b': 'unsupported feature'})
        self.assertTrue(result['ok'])
        self.assertEqual((result['passed'], result['failed'], result['allowed']), (1, 0, 1))
    def test_improvement_is_reported_without_failing_regression_gate(self):
        self.assertTrue(acceptance.classify({'b': True}, {'b': 'unsupported feature'})['ok'])
    def test_stale_or_empty_allowlist_entry_fails(self):
        self.assertFalse(acceptance.classify({'a': True}, {'b': 'unsupported'})['ok'])
        self.assertFalse(acceptance.classify({'b': False}, {'b': ''})['ok'])
    def test_runner_errors_cannot_be_allowlisted(self):
        self.assertFalse(acceptance.classify({'b': False}, {'b': 'unsupported'}, ['b'])['ok'])
    def test_zero_tests_never_pass(self):
        self.assertFalse(acceptance.classify({}, {})['ok'])
if __name__ == '__main__': unittest.main()
