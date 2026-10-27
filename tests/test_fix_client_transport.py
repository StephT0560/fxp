# SPDX-License-Identifier: MIT
import importlib.util
import socket
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('fix_client', 'fix_test_client/fix_client.py')
fix = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fix)


class FragmentedSocket:
    def __init__(self, parts):
        self.parts = list(parts)

    def recv(self, count):
        part = self.parts.pop(0) if self.parts else b''
        if isinstance(part, Exception):
            raise part
        if len(part) > count:
            self.parts.insert(0, part[count:])
        return part[:count]


class FixTransportTest(unittest.TestCase):
    def test_timeout_at_every_frame_boundary_preserves_frame_and_next_message(self):
        first = fix.build_message('8', 'FXPBRIDGE', 'TEST', 1, [(11, 'partial'), (150, '0')])
        second = fix.build_message('r', 'FXPBRIDGE', 'TEST', 2, [(11, 'mass'), (531, '7')])
        for offset in range(1, len(first)):
            with self.subTest(offset=offset):
                sock = FragmentedSocket([first[:offset], socket.timeout(), first[offset:] + second])
                reader = fix.FIXReader(sock)
                with self.assertRaises(socket.timeout):
                    reader.read()
                self.assertEqual(reader.read()[11], 'partial')
                self.assertEqual(reader.read()[11], 'mass')
                self.assertIsNone(reader.read())

    def test_synchronous_reader_does_not_consume_the_next_frame(self):
        frame = fix.build_message('A', 'FXPBRIDGE', 'TEST', 1, [])
        sock = FragmentedSocket([frame + frame])
        self.assertEqual(fix.read_message(sock)[35], 'A')
        self.assertEqual(fix.read_message(sock)[35], 'A')
        with self.assertRaises(socket.timeout):
            fix.read_message(FragmentedSocket([socket.timeout()]))

    def test_truncated_or_malformed_frame_fails_loudly(self):
        frame = fix.build_message('A', 'FXPBRIDGE', 'TEST', 1, [])
        with self.assertRaises(EOFError):
            fix.FIXReader(FragmentedSocket([frame[:-1]])).read()
        with self.assertRaises(ValueError):
            fix.FIXReader(FragmentedSocket([b'8=FIX.4.4\x019=-1\x01'])).read()

    def test_session_worker_owns_reads_and_writes_and_waits_for_logon(self):
        class Transport(FragmentedSocket):
            def __init__(self):
                super().__init__([])
                self.owners = set()

            def settimeout(self, value):
                pass

            def recv(self, count):
                self.owners.add(threading.get_ident())
                if not self.parts:
                    # Model a bounded socket read without a network dependency.
                    threading.Event().wait(0.001)
                    raise socket.timeout()
                return super().recv(count)

            def sendall(self, frame):
                self.owners.add(threading.get_ident())
                kind = fix.parse_message(frame)[35]
                if kind == 'A':
                    self.parts.append(fix.build_message('A', 'FXPBRIDGE', 'TEST', 1, []))

        transport = Transport()
        session = fix.FIXSession('localhost', 0, 'TEST', 'FXPBRIDGE')
        with patch.object(fix, 'connect_socket', return_value=transport):
            session.connect()
        try:
            session.logon()
            self.assertTrue(session._logon.is_set())
            session.send('0', [])
            self.assertEqual(transport.owners, {session._thread.ident})
            self.assertNotIn(threading.get_ident(), transport.owners)
        finally:
            session._stop.set()
            session._thread.join(timeout=1)
        self.assertFalse(session._thread.is_alive())

    def test_missing_logon_is_a_failure(self):
        session = fix.FIXSession('localhost', 0, 'TEST', 'FXPBRIDGE')
        with self.assertRaisesRegex(TimeoutError, 'Logon acknowledgement missing'):
            session.wait_for_logon(timeout=0)


if __name__ == '__main__':
    unittest.main()
