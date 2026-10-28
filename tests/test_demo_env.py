# SPDX-License-Identifier: MIT
import contextlib
import importlib.util
import io
import os
import stat
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import mock_open, patch

spec = importlib.util.spec_from_file_location('demo_env', 'scripts/demo_env.py')
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


class DemoEnvTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = os.path.join(self.directory.name, '.env.demo')

    def present(self, mode):
        with open(self.path, 'w') as output:
            output.write('existing credentials\n')
        os.chmod(self.path, mode)

    def test_absent_created_private_without_changing_secret_format(self):
        with patch.object(demo.secrets, 'token_hex', side_effect=['a' * 64, 'b' * 48]) as tokens:
            demo.ensure_demo_env(self.path, create=True)
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        self.assertEqual(tokens.call_args_list, [unittest.mock.call(32), unittest.mock.call(24)])
        with open(self.path) as output:
            self.assertEqual(output.read(), 'FXP_JWT_SECRET=' + 'a' * 64 + '\nFXP_FIX_CREDENTIALS=DEMO:' + 'b' * 48 + '\n')

    def test_existing_private_is_accepted_unchanged(self):
        self.present(0o600)
        with patch.object(demo.secrets, 'token_hex') as tokens:
            demo.ensure_demo_env(self.path, create=True)
            tokens.assert_not_called()
        with open(self.path) as output:
            self.assertEqual(output.read(), 'existing credentials\n')

    def test_posix_public_file_refused_without_chmod(self):
        self.present(0o644)
        with patch.object(demo, 'windows_mount', return_value=False), patch.object(demo.os, 'chmod') as chmod:
            with self.assertRaisesRegex(SystemExit, 'mode 600'):
                demo.ensure_demo_env(self.path)
            chmod.assert_not_called()

    def test_windows_skips_only_modes_and_warns(self):
        self.present(0o644)
        warning = io.StringIO()
        with patch.object(demo.os, 'name', 'nt'), contextlib.redirect_stderr(warning):
            demo.ensure_demo_env(self.path)
        self.assertIn('not protected by POSIX file permissions', warning.getvalue())
        self.assertEqual(len(warning.getvalue().splitlines()), 1)

    def test_windows_still_refuses_symlinks_and_directories(self):
        for dangling in [False, True]:
            target = os.path.join(self.directory.name, 'missing' if dangling else 'target')
            if not dangling:
                with open(target, 'w') as output:
                    output.write('credentials')
            os.symlink(target, self.path)
            with patch.object(demo.os, 'name', 'nt'), self.assertRaises(SystemExit):
                demo.ensure_demo_env(self.path, create=True)
            os.unlink(self.path)
        os.mkdir(self.path)
        with patch.object(demo.os, 'name', 'nt'), self.assertRaises(SystemExit):
            demo.ensure_demo_env(self.path)

    def test_wsl_chmod_probe_accepts_real_permissions(self):
        self.present(0o644)
        warning = io.StringIO()
        with patch.object(demo, 'windows_mount', return_value=True), contextlib.redirect_stderr(warning):
            demo.ensure_demo_env(self.path)
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        self.assertEqual(warning.getvalue(), '')

    def test_wsl_emulated_modes_warn_when_chmod_has_no_effect(self):
        self.present(0o644)
        warning = io.StringIO()
        with patch.object(demo, 'windows_mount', return_value=True), patch.object(demo.os, 'chmod') as chmod, contextlib.redirect_stderr(warning):
            demo.ensure_demo_env(self.path)
        chmod.assert_called_once_with(self.path, 0o600)
        self.assertIn('not protected by POSIX file permissions', warning.getvalue())

    def test_wsl_partial_mode_change_still_refuses_public_file(self):
        self.present(0o644)
        chmod = os.chmod
        with patch.object(demo, 'windows_mount', return_value=True), patch.object(demo.os, 'chmod', side_effect=lambda path, mode: chmod(path, 0o640)):
            with self.assertRaisesRegex(SystemExit, 'mode 600'):
                demo.ensure_demo_env(self.path)

    def test_up_and_down_validate_before_compose_reads_credentials(self):
        scripts = os.path.join(self.directory.name, 'scripts')
        tools = os.path.join(self.directory.name, 'tools')
        os.mkdir(scripts)
        os.mkdir(tools)
        for filename in ['demo.sh', 'demo_env.py']:
            shutil.copyfile(os.path.join('scripts', filename), os.path.join(scripts, filename))
        docker = os.path.join(tools, 'docker')
        with open(docker, 'w') as output:
            output.write('#!/bin/sh\nprintf "called\\n" >> docker.calls\n')
        os.chmod(docker, 0o755)
        env = dict(os.environ, PATH=tools + os.pathsep + os.environ['PATH'])
        for command in ['up', 'down']:
            result = subprocess.run(['bash', os.path.join(scripts, 'demo.sh'), command],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        os.chmod(self.path, 0o644)
        for command in ['up', 'down']:
            result = subprocess.run(['bash', os.path.join(scripts, 'demo.sh'), command],
                                    env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('mode 600', result.stderr)
        with open(os.path.join(self.directory.name, 'docker.calls')) as calls:
            self.assertEqual(calls.read().splitlines(), ['called', 'called'])

    def test_mount_detection_requires_wsl_and_windows_filesystem(self):
        mounts = 'none / ext4 rw 0 0\nC: /mnt/c 9p rw,aname=drvfs;path=C: 0 0\nnone /mnt/c/linux ext4 rw 0 0\n'
        with patch.object(demo.platform, 'release', return_value='microsoft-standard-WSL2'), patch('builtins.open', mock_open(read_data=mounts)):
            self.assertTrue(demo.windows_mount('/mnt/c/repo/.env.demo'))
            self.assertFalse(demo.windows_mount('/home/user/repo/.env.demo'))
            self.assertFalse(demo.windows_mount('/mnt/c/linux/.env.demo'))
        with patch.object(demo.platform, 'release', return_value='generic Linux'):
            self.assertFalse(demo.windows_mount('/mnt/c/repo/.env.demo'))


if __name__ == '__main__':
    unittest.main()
