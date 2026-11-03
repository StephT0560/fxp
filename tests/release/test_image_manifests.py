# SPDX-License-Identifier: MIT
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_manifest', ROOT / 'scripts/release_manifest.py')
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class ImageManifestTests(unittest.TestCase):
    def test_publish_uses_both_platforms_and_records_index_not_config_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            architecture_digests = {a: 'sha256:' + str(i + 1) * 64 for i, a in enumerate(images.ARCHES)}
            for arch in images.ARCHES:
                (root / f'{arch}.json').write_text(json.dumps({'architecture': arch, 'images':
                    {name: architecture_digests[arch] for name in images.COMPONENTS}}))
            index = 'sha256:' + 'a' * 64
            description = 'Name: ghcr.io/example/fxp/image:v0.1.0\nDigest:    ' + index + '\n'
            raw = json.dumps({'manifests': [
                {'platform': {'os': 'linux', 'architecture': a}, 'digest': architecture_digests[a]}
                for a in images.ARCHES]})
            with patch.object(images.subprocess, 'run') as run, patch.object(
                    images.subprocess, 'check_output', side_effect=[description, raw] * 5):
                images.publish(root, 'ghcr.io/example/fxp', 'v0.1.0', 'b' * 40,
                               root / 'notes.md', root / 'images.json')
                self.assertEqual(run.call_count, 5)
                for call in run.call_args_list:
                    argv = call.args[0]
                    self.assertEqual(argv.count('-t'), 2)
                    self.assertIn('@' + architecture_digests['amd64'], argv[-2])
                    self.assertIn('@' + architecture_digests['arm64'], argv[-1])
            lock = json.loads((root / 'images.json').read_text())
            self.assertEqual(set(lock['images']), set(images.COMPONENTS))
            self.assertTrue(all(v['image'].endswith('@' + index) for v in lock['images'].values()))
            self.assertIn(index, (root / 'notes.md').read_text())
            (root / 'arm64.json').unlink()
            with patch.object(images.subprocess, 'run') as run, self.assertRaises(FileNotFoundError):
                images.publish(root, 'ghcr.io/example/fxp', 'v0.1.0', 'b' * 40,
                               root / 'notes.md', root / 'images.json')
            run.assert_not_called()

    def test_invalid_coordinates_and_untrusted_digest_artifacts_fail_closed(self):
        for args in [('example.com/owner/repo', 'v0.1.0', 'a' * 40),
                     ('ghcr.io/owner/repo', 'v0.1.0;echo bad', 'a' * 40),
                     ('ghcr.io/owner/repo', 'v0.1.0', 'not-a-sha')]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                images.coordinates(*args)
        for bad in ['sha256:abc', 'a' * 64, '\nsha256:' + 'a' * 64, None]:
            with self.subTest(digest=bad), self.assertRaises(ValueError):
                images.digest(bad)
        with tempfile.TemporaryDirectory() as temporary:
            p = Path(temporary) / 'artifact.json'
            p.write_text('{"images":{},"images":{}}')
            with self.assertRaises(ValueError):
                images.read_json(p)
            p.write_bytes(b'x' * (1024 * 1024 + 1))
            with self.assertRaises(ValueError):
                images.read_json(p)
