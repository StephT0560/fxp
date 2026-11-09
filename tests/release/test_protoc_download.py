# SPDX-License-Identifier: MIT
"""Pinned build dependency transport and integrity are shared by CI and images."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]

class ProtocDownload(unittest.TestCase):
    def source(self):
        installer=ROOT/'scripts/install-protoc.sh'
        return installer.read_text() if installer.exists() else (ROOT/'deploy/Dockerfile.release').read_text().split('AS protoc',1)[1].split('\nFROM ',1)[0]

    def test_explicit_official_download_route_and_architecture_pins(self):
        source=self.source()
        self.assertIn('protoc-25.3-linux-${arch}.zip?download=1',source)
        for digest in ('f853e691868d0557425ea290bf7ba6384eef2fa9b04c323afab49a770ba9da80','9eae1f20f70cccc912d1c318c3929b86aebf5afd4b0f32c196ef682c222ed5ae'):
            self.assertIn(digest,source)
        self.assertLess(source.index('sha256sum -c -'),source.index('unzip -q'))
        self.assertNotIn('--insecure',source)

    def test_ci_and_images_use_the_same_verified_installer(self):
        for name in ('ci.yml','release.yml','bench.yml'):
            workflow=(ROOT/'.github/workflows'/name).read_text()
            self.assertTrue('arduino/setup-protoc@' not in workflow,'CI must use the verified shared installer')
            self.assertIn('scripts/install-protoc.sh',workflow)
        self.assertIn('scripts/install-protoc.sh',(ROOT/'deploy/Dockerfile.release').read_text())

    def test_corrupt_download_is_refused_before_extraction(self):
        import os,subprocess,tempfile
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);tools=root/'tools';tools.mkdir()
            curl=tools/'curl';curl.write_text('#!/usr/bin/python3\nimport pathlib,sys\npathlib.Path(sys.argv[sys.argv.index("-o")+1]).write_bytes(b"corrupt compiler")\n');curl.chmod(0o755)
            unzip=tools/'unzip';unzip.write_text('#!/bin/sh\ntouch "$UNZIP_MARKER"\n');unzip.chmod(0o755)
            marker=root/'extracted';destination=root/'protoc'
            result=subprocess.run(['sh',str(ROOT/'scripts/install-protoc.sh'),str(destination)],env={**os.environ,'TARGETARCH':'amd64','PATH':str(tools)+os.pathsep+os.environ['PATH'],'UNZIP_MARKER':str(marker)},capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0)
            self.assertIn("FAILED",result.stdout)
            self.assertFalse(marker.exists());self.assertFalse(destination.exists())
