#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Create or validate the local demo credentials before any reader uses them."""
import argparse
import os
import platform
import re
import secrets
import stat
import sys


def windows_mount(path):
    """Only WSL drvfs mounts may emulate Unix modes; ordinary POSIX stays strict."""
    if 'microsoft' not in platform.release().lower():
        return False
    try:
        with open('/proc/mounts') as mounts:
            entries = [line.split() for line in mounts]
    except OSError:
        return False
    actual = os.path.realpath(path)
    best = None
    for fields in entries:
        if len(fields) < 4:
            continue
        mount = re.sub(r'\\([0-7]{3})', lambda m: chr(int(m[1], 8)), fields[1])
        if actual == mount or actual.startswith(mount.rstrip('/') + '/'):
            if best is None or len(mount) > len(best[0]):
                best = (mount, fields[2], fields[3])
    return best is not None and (best[1] == 'drvfs' or
                                best[1] == '9p' and 'aname=drvfs' in best[2])


def ensure_demo_env(path='.env.demo', create=False):
    if create:
        try:
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            pass
        else:
            with os.fdopen(descriptor, 'w') as output:
                output.write('FXP_JWT_SECRET=' + secrets.token_hex(32) + '\n')
                # Required by the base file's interpolation even while FIX is disabled.
                output.write('FXP_FIX_CREDENTIALS=DEMO:' + secrets.token_hex(24) + '\n')
    mode = os.lstat(path).st_mode
    if not stat.S_ISREG(mode):  # lstat also rejects symlinks, including dangling ones.
        raise SystemExit(f'{path} must be a regular private file (mode 600)')
    if os.name == 'nt':
        unprotected = True
    elif mode & 0o077:
        if not windows_mount(path):
            raise SystemExit(f'{path} must be a regular private file (mode 600)')
        original_mode = stat.S_IMODE(mode)
        os.chmod(path, 0o600)
        mode = os.lstat(path).st_mode
        if not stat.S_ISREG(mode):
            raise SystemExit(f'{path} must be a regular private file (mode 600)')
        if mode & 0o077 and stat.S_IMODE(mode) != original_mode:
            raise SystemExit(f'{path} must be a regular private file (mode 600)')
        unprotected = bool(mode & 0o077)
    else:
        unprotected = False
    if unprotected:
        print(f'Warning: {path} credentials are not protected by POSIX file permissions on this filesystem.', file=sys.stderr)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--create', action='store_true')
    args = parser.parse_args()
    ensure_demo_env(create=args.create)
