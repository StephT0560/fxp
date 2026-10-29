#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Exercise the actual build.rs watches using a dependency-free Cargo fixture."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BuildProvenance(unittest.TestCase):
    def run_command(self, cwd, *args):
        result = subprocess.run(args, cwd=cwd, env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout.strip()

    def setUp(self):
        (ROOT / "work").mkdir(exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(prefix="git-provenance-", dir=ROOT / "work")
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name) / "repo"
        self.repo.mkdir()
        self.env = os.environ.copy()
        self.env.pop("FXP_BUILD_GIT_SHA", None)
        self.env["CARGO_TARGET_DIR"] = str(Path(self.tmp.name) / "target")
        self.run_command(self.repo, "git", "init", "-b", "main")
        self.run_command(self.repo, "git", "config", "user.name", "Provenance Test")
        self.run_command(self.repo, "git", "config", "user.email", "test@example.invalid")
        crate = self.repo / "fxp_core"
        (crate / "src").mkdir(parents=True)
        (crate / "proto").mkdir()
        (crate / "Cargo.toml").write_text('[package]\nname="provenance_fixture"\nversion="0.0.0"\nedition="2024"\n')
        (crate / "src/main.rs").write_text('fn main() { println!("{}|{}", env!("FXP_BUILD_GIT_SHA"), env!("SCHEMA_TEST_CONTENT")); }\n')
        (crate / "proto/fxp.proto").write_text("schema-A")
        source = (ROOT / "fxp_core/build.rs").read_text()
        # Replace only protoc invocation; retain the production Git/env/watch logic.
        source, count = re.subn(r'    prost_build::Config::new\(\).*?\.unwrap\(\);',
                               '    println!("cargo:rustc-env=SCHEMA_TEST_CONTENT={}", std::fs::read_to_string("proto/fxp.proto").unwrap());',
                               source, flags=re.S)
        self.assertEqual(count, 1)
        (crate / "build.rs").write_text(source)
        self.run_command(self.repo, "git", "add", ".")
        self.run_command(self.repo, "git", "commit", "-m", "fixture A")

    def check_build(self, repo, schema):
        sha = self.run_command(repo, "git", "rev-parse", "HEAD")
        actual = self.run_command(repo, "cargo", "run", "--quiet", "--offline", "--manifest-path", "fxp_core/Cargo.toml")
        self.assertEqual(actual, f"{sha}|{schema}")

    def exercise(self, repo):
        self.check_build(repo, "schema-A")
        # A dirty schema must regenerate even without a Git ref change.
        (repo / "fxp_core/proto/fxp.proto").write_text("schema-B")
        self.check_build(repo, "schema-B")
        self.run_command(repo, "git", "add", "fxp_core/proto/fxp.proto")
        self.run_command(repo, "git", "commit", "-m", "fixture B")
        self.check_build(repo, "schema-B")
        self.run_command(repo, "git", "pack-refs", "--all", "--prune")
        self.check_build(repo, "schema-B")
        self.run_command(repo, "git", "commit", "--allow-empty", "-m", "fixture C")
        self.check_build(repo, "schema-B")
        self.run_command(repo, "git", "checkout", "--detach", "HEAD~1")
        self.check_build(repo, "schema-B")
        self.env["FXP_BUILD_GIT_SHA"] = "archive-build-override"
        actual = self.run_command(repo, "cargo", "run", "--quiet", "--offline", "--manifest-path", "fxp_core/Cargo.toml")
        self.assertEqual(actual, "archive-build-override|schema-B")

    def test_normal_checkout(self):
        self.exercise(self.repo)

    def test_linked_worktree(self):
        linked = Path(self.tmp.name) / "linked"
        self.run_command(self.repo, "git", "worktree", "add", "-b", "linked", str(linked))
        self.exercise(linked)


if __name__ == "__main__":
    unittest.main(verbosity=2)
