# SPDX-License-Identifier: MIT
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from fxp_client import pb
from gallery.bots import SharedOwnerBot, LateReaderBot


class VariantTests(unittest.TestCase):
    def test_operator_jwt_matches_independent_go_vector(self):
        from gallery.scenarios import operator_token
        import os
        with patch.dict(os.environ, {'FXP_JWT_SECRET': 'a' * 32, 'FXP_JWT_ISSUER': 'test',
                                     'FXP_JWT_AUDIENCE': 'gateway', 'FXP_JWT_KID': 'key'}), \
             patch('fxp_client.client.time.time', return_value=1700000000), \
             patch('fxp_client.client.secrets.token_hex', return_value='test-jti'):
            actual = operator_token()
        vector = Path(__file__).resolve().parents[3]/'sdk/python/tests/golden/operator-token.txt'
        self.assertEqual(actual, vector.read_text().strip())

    def test_all_four_seeded_entries_and_blind_selections_are_registered(self):
        from gallery.blind import BUGS, pick
        root = Path(__file__).parent
        self.assertEqual({pick(seed) for seed in range(100)}, set(BUGS))
        for bug in BUGS:
            entry = json.loads((root/bug/'entry.json').read_text())
            self.assertEqual(entry['bug'], bug)
            self.assertEqual(entry['seed'], 7)
            self.assertEqual(entry['bot_variant'], 'BUG=' + bug)
            self.assertTrue(entry['symptom'] and entry['root_cause'])

    def test_stp_cancel_is_deliberately_miscounted_as_inventory(self):
        bot = SharedOwnerBot()
        bot.observe(pb.ExecutionReport(execution_type=pb.EXEC_CANCELED, order_quantity=4))
        self.assertEqual(bot.inventory, -4)

    def test_late_fill_then_cancel_reject_double_hedges(self):
        bot = LateReaderBot()
        for kind in (pb.EXEC_FILLED, pb.EXEC_REJECTED):
            bot.observe(pb.ExecutionReport(execution_type=kind))
        self.assertEqual(bot.hedges, 2)

    def test_blind_output_is_only_path_and_symptom_and_reveal_uses_seal(self):
        from gallery.blind import blind, reveal, pick
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seal = root/'seal.json'
            def run_trial(bug, seed, folder):
                (folder/'bundle').mkdir()
                (folder/'scenario.json').write_text(json.dumps({'symptom': 'Position drift observed.'}))
                (folder/'diagnosis.txt').write_text('secret diagnosis')
                return 'secret diagnosis', 'Position drift observed.'
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                blind(7, root, seal, runner=run_trial, clock=lambda: 100)
            lines = output.getvalue().splitlines()
            self.assertEqual(len(lines), 2)
            self.assertTrue(lines[0].startswith('Bundle: '))
            self.assertEqual(lines[1], 'Symptom: Position drift observed.')
            self.assertNotIn('secret', output.getvalue())
            self.assertFalse((Path(lines[0][8:]).parent/'scenario.json').exists())
            self.assertEqual(seal.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(seal.read_text())['bug'], pick(7))
            with self.assertRaises(FileExistsError):
                blind(7, root, seal, runner=run_trial)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                reveal(seal, clock=lambda: 145)
            self.assertIn('secret diagnosis', output.getvalue())
            self.assertIn('45.0 seconds', output.getvalue())
            self.assertFalse(seal.exists())
