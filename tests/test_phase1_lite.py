# SPDX-License-Identifier: MIT
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'scripts'))
from phase1_lite import syscall_counts

class Phase1Tests(unittest.TestCase):
 def test_syscall_parser_excludes_files_and_trades_and_handles_resumption(self):
  raw=r'''
10 write(7<TCP:[1]>, "\x00\x00\x00\x12\x52\x10"..., 22) = 22
11 write(8<TCP:[2]>, "\x00\x00\x00\x12\x52\x10"..., 22 <unfinished ...>
11 <... write resumed>) = -1 EAGAIN (Resource temporarily unavailable)
12 write(9<TCP:[3]>, "\x00\x00\x00\x12\x62\x10"..., 22) = 22
13 write(4</tmp/file>, "\x00\x00\x00\x12\x52\x10"..., 22) = 22
14 write(10<TCP:[4]>, "\x00\x00\x00\x12\x52\x10"..., 22) = 8
'''
  with tempfile.TemporaryDirectory() as directory:
   path=Path(directory)/'trace';path.write_text(raw)
   counts=syscall_counts(path)
  self.assertEqual(counts['incremental_write_syscall_attempts'],3)
  self.assertEqual(counts['errors'],1)
  self.assertEqual(counts['short_writes'],1)
  self.assertEqual(counts['complete_writes'],1)
