# SPDX-License-Identifier: MIT
"""Compressed HTML must preserve the complete report, including provenance."""
import base64
import importlib.util
import json
from pathlib import Path
import unittest
import zlib

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_smoke_payload', ROOT / 'scripts/release_smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def page(data):
    return '<script id="report-data" type="application/json">' + json.dumps(data) + '</script>'


def encoded(data):
    raw = json.dumps(data).encode()
    return {'encoding': 'zlib-base64-v1', 'decoded_bytes': str(len(raw)),
            'data': base64.b64encode(zlib.compress(raw)).decode()}


class ReportPayloadTests(unittest.TestCase):
    def test_plain_and_compressed_reports_preserve_all_values(self):
        report = {'schema': 'fxp-run-report-v1', 'manifest': {'meta': {'images': {'core': 'image@sha256:' + 'a' * 64}}}}
        self.assertEqual(smoke.report_payload(page(report)), report)
        self.assertEqual(smoke.report_payload(page(encoded(report))), report)

    def test_malformed_or_unbounded_payloads_are_rejected(self):
        good = encoded({'schema': 'fxp-run-report-v1'})
        cases = [dict(good, decoded_bytes='100663297'), dict(good, decoded_bytes='01'),
                 dict(good, decoded_bytes='1'), dict(good, data='not base64'),
                 dict(good, data=base64.b64encode(base64.b64decode(good['data']) + b'trailing').decode()),
                 encoded(good)]
        for data in cases:
            with self.subTest(kind=list(data)):
                with self.assertRaises(ValueError):
                    smoke.report_payload(page(data))
        with self.assertRaises(ValueError):
            smoke.report_payload(page(good) + page(good))
        with self.assertRaises(ValueError):
            smoke.report_payload('<script id="report-data" type="application/json">{"manifest":{},"manifest":{}}</script>')
