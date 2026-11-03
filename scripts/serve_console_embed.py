#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Serve the static embed example and built console on loopback, with no APIs."""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import mimetypes
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
EXAMPLE = ROOT / 'examples/console-embed'
DIST = ROOT / 'web/dist'


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = unquote(urlsplit(self.path).path)
        fixed = {'/': EXAMPLE / 'index.html', '/index.html': EXAMPLE / 'index.html',
                 '/host.js': EXAMPLE / 'host.js', '/host.css': EXAMPLE / 'host.css',
                 '/console.html': DIST / 'index.html', '/favicon.svg': DIST / 'favicon.svg'}
        source = fixed.get(path)
        if source is None and path.startswith('/assets/'):
            candidate = (DIST / path.lstrip('/')).resolve()
            if candidate.is_relative_to((DIST / 'assets').resolve()):
                source = candidate
        if source is None or not source.is_file():
            self.send_error(404)
            return
        content = source.read_bytes()
        self.send_response(200)
        self.send_header('Content-Type', mimetypes.guess_type(source)[0] or 'application/octet-stream')
        self.send_header('Content-Length', str(len(content)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Referrer-Policy', 'no-referrer')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' ws://127.0.0.1:8081 ws://localhost:8081; frame-src 'self' about:; frame-ancestors 'self'; object-src 'none'; base-uri 'none'; form-action 'none'")
        self.end_headers()
        self.wfile.write(content)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port', type=int, default=3100)
    args = parser.parse_args()
    if not (DIST / 'index.html').is_file():
        raise SystemExit('Build the console first: cd web && npm run build')
    server = ThreadingHTTPServer(('127.0.0.1', args.port), Handler)
    print(f'FXP embed example on loopback port {server.server_port}', flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
