#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Verify architecture and absence of PT_INTERP in ELF64 Linux release binaries."""
from pathlib import Path
import struct
import sys


def verify(path, arch):
    data = Path(path).read_bytes()
    if data[:7] != b'\x7fELF\x02\x01\x01':
        raise ValueError(f'{path}: expected little-endian ELF64')
    machine = struct.unpack_from('<H', data, 18)[0]
    if machine != {'amd64': 62, 'arm64': 183}[arch]:
        raise ValueError(f'{path}: wrong machine {machine}')
    offset = struct.unpack_from('<Q', data, 32)[0]
    entry, count = struct.unpack_from('<HH', data, 54)
    if not count or entry < 56 or offset + entry * count > len(data):
        raise ValueError(f'{path}: invalid program headers')
    for i in range(count):
        if struct.unpack_from('<I', data, offset + i * entry)[0] in (2, 3):
            # PT_DYNAMIC is permitted for a static PIE; inspect its DT_NEEDED.
            if struct.unpack_from('<I', data, offset + i * entry)[0] == 3:
                raise ValueError(f'{path}: dynamically linked PT_INTERP')
            body, size = struct.unpack_from('<Q', data, offset + i * entry + 8)[0], struct.unpack_from('<Q', data, offset + i * entry + 32)[0]
            if body + size > len(data):
                raise ValueError(f'{path}: invalid dynamic section')
            for pos in range(body, body + size, 16):
                tag = struct.unpack_from('<Q', data, pos)[0]
                if tag == 0:
                    break
                if tag == 1:
                    raise ValueError(f'{path}: DT_NEEDED shared-library dependency')
    print(f'{path}: static ELF64 Linux {arch}')


if __name__ == '__main__':
    for binary in sys.argv[2:]:
        verify(binary, sys.argv[1])
