# SPDX-License-Identifier: MIT
import importlib.util
from pathlib import Path
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('verify_static_elf', Path(__file__).resolve().parents[2] / 'scripts/verify_static_elf.py')
elf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(elf)


class StaticELFTest(unittest.TestCase):
    def fixture(self, machine=62, segment=1, needed=False):
        data = bytearray(136)
        data[:7] = b'\x7fELF\x02\x01\x01'
        struct.pack_into('<H', data, 18, machine)
        struct.pack_into('<Q', data, 32, 64)
        struct.pack_into('<HH', data, 54, 56, 1)
        struct.pack_into('<I', data, 64, segment)
        if segment == 2:
            struct.pack_into('<Q', data, 72, 120)
            struct.pack_into('<Q', data, 96, 16)
            struct.pack_into('<Q', data, 120, 1 if needed else 0)
        return data

    def check(self, data, arch='amd64'):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'binary'
            path.write_bytes(data)
            elf.verify(path, arch)

    def test_static_and_static_pie(self):
        self.check(self.fixture())
        self.check(self.fixture(segment=2))
        self.check(self.fixture(machine=183), 'arm64')

    def test_interpreter_and_needed_libraries_are_rejected(self):
        for data in [self.fixture(segment=3), self.fixture(segment=2, needed=True)]:
            with self.assertRaises(ValueError):
                self.check(data)

    def test_wrong_architecture_and_truncation_are_rejected(self):
        for data in [self.fixture(machine=183), self.fixture()[:80]]:
            with self.assertRaises(ValueError):
                self.check(data)


if __name__ == '__main__':
    unittest.main()
