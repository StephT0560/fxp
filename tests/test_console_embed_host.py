# SPDX-License-Identifier: MIT
"""Static example hosting is loopback-only and never serves arbitrary repo files."""
import importlib.util
from pathlib import Path
import tempfile
from threading import Thread
import unittest
from urllib.error import HTTPError
from urllib.request import urlopen
from http.server import ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('embed_host', ROOT / 'scripts/serve_console_embed.py')
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)


class EmbedHostTests(unittest.TestCase):
    def test_built_console_bootstrap_and_asset_path_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            example, dist = root / 'example', root / 'dist'
            example.mkdir(); (dist / 'assets').mkdir(parents=True)
            (example / 'index.html').write_text('host fixture')
            (example / 'bootstrap.js').write_text('/* trusted prelude */')
            (dist / 'index.html').write_text('<html><head></head><body>console</body></html>')
            (dist / 'assets/app.js').write_text('/* app */')
            (root / 'private').write_text('must not be served')
            (dist / 'assets/escape').symlink_to(root / 'private')
            old_example, old_dist = host.EXAMPLE, host.DIST
            host.EXAMPLE, host.DIST = example, dist
            server = ThreadingHTTPServer(('127.0.0.1', 0), host.Handler)
            thread = Thread(target=server.serve_forever, daemon=True); thread.start()
            base = f'http://127.0.0.1:{server.server_port}'
            try:
                with urlopen(base + '/console.html', timeout=2) as response:
                    self.assertIn(b'<script src="/bootstrap.js"></script>', response.read())
                    self.assertIn("frame-ancestors 'self'", response.headers['Content-Security-Policy'])
                    self.assertEqual(response.headers['Cache-Control'], 'no-store')
                with urlopen(base + '/assets/app.js', timeout=2) as response:
                    self.assertEqual(response.read(), b'/* app */')
                for path in ['/private', '/.git/config', '/assets/%2e%2e/index.html', '/assets/escape']:
                    with self.subTest(path=path), self.assertRaises(HTTPError) as failure:
                        urlopen(base + path, timeout=2)
                    self.assertEqual(failure.exception.code, 404)
            finally:
                server.shutdown(); server.server_close(); thread.join(timeout=2)
                host.EXAMPLE, host.DIST = old_example, old_dist
