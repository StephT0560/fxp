# SPDX-License-Identifier: MIT
"""Fresh checkouts lack ignored Go bindings in both measurement sources."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import subscriber_isolation as collector


class FreshCheckoutTest(unittest.TestCase):
    def test_each_checkout_generates_bindings_before_build(self):
        class PreparationComplete(Exception):
            pass

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            base = root/'work/subscriber-isolation/before-source'
            generated = set()
            built = []

            def run(command, *, cwd=root, **_kwargs):
                if command[:2] == ['git', 'rev-parse']:
                    return b'test-revision\n'
                if command[:2] == ['git', 'worktree']:
                    base.mkdir()
                elif command == ['bash', 'scripts/gen_proto.sh']:
                    generated.add(cwd)
                elif command[:2] == ['go', 'build']:
                    self.assertIn(cwd.parent, generated,
                                  'Go build started without checkout-local ignored bindings')
                    built.append(cwd.parent)
                elif command[:2] == ['cargo', 'build']:
                    # Stop before measurement/build costs: test the real startup
                    # sequence, not subprocess tooling or benchmark internals.
                    raise PreparationComplete
                return b''

            with patch.object(collector, 'ROOT', root), patch.object(collector, 'run', run), patch.object(sys, 'argv', ['subscriber_isolation.py']):
                with self.assertRaises(PreparationComplete):
                    collector.main()
            self.assertEqual(generated, {root, base})
            self.assertEqual(built, [root])


if __name__ == '__main__':
    unittest.main()
