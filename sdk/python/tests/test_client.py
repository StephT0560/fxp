# SPDX-License-Identifier: MIT
import base64
import hashlib
import hmac
import json
from pathlib import Path
import socket
import struct
import unittest
from unittest.mock import patch

from fxp_client import Client, ProtocolError, TradeGap, TradeSequences, dev_token, pb
from fxp_client.client import encode_frame, read_frame

GOLDEN = Path(__file__).parent / "golden"


class Fragmented:
    def __init__(self, data):
        self.data = data

    def recv(self, n):
        result, self.data = self.data[:min(n, 2)], self.data[min(n, 2):]
        return result


class ClientTests(unittest.TestCase):
    def test_go_golden_frames_decode_and_encode(self):
        expected = json.loads((GOLDEN / "fields.json").read_text())
        for name, fields in expected.items():
            with self.subTest(name=name):
                frame = (GOLDEN / f"{name}.bin").read_bytes()
                payload = read_frame(Fragmented(frame))
                msg = pb.FXPMessage.FromString(payload)
                self.assertEqual(msg.WhichOneof("payload"), fields["kind"])
                body = getattr(msg, fields["kind"])
                for key, value in fields["fields"].items():
                    self.assertEqual(getattr(body, key), value)
                self.assertEqual(encode_frame(msg), frame)
        self.assertFalse(pb.FXPMessage.FromString(read_frame(Fragmented((GOLDEN / 'replace-absent.bin').read_bytes()))).order_cancel_replace.HasField('new_display_quantity'))
        self.assertTrue(pb.FXPMessage.FromString(read_frame(Fragmented((GOLDEN / 'replace-zero.bin').read_bytes()))).order_cancel_replace.HasField('new_display_quantity'))

    def test_fragmentation_and_bad_frames(self):
        self.assertEqual(read_frame(Fragmented(b'\0\0\0\3abc')), b'abc')
        with self.assertRaises(EOFError):
            read_frame(Fragmented(b''))
        for raw in (b'\0', b'\0\0\0\3ab', b'\0\0\0\0', struct.pack('!I', 65537)):
            with self.subTest(raw=raw), self.assertRaises(ProtocolError):
                read_frame(Fragmented(raw))
        with self.assertRaises(ProtocolError):
            encode_frame(pb.FXPMessage())
        with self.assertRaises(ProtocolError):
            encode_frame(pb.FXPMessage(logon=pb.Logon(token='x' * 70000)))

    def test_trade_sequences_baseline_per_symbol_gap_duplicate_regression(self):
        seq = TradeSequences()
        self.assertIsNone(seq.observe('A', 42))  # joining a live feed is not a gap
        self.assertIsNone(seq.observe('B', 1))
        self.assertIsNone(seq.observe('A', 43))
        self.assertEqual(seq.observe('A', 46), TradeGap('A', 44, 46))
        self.assertEqual(seq.observe('A', 46), TradeGap('A', 47, 46))
        self.assertEqual(seq.observe('A', 45), TradeGap('A', 47, 45))
        self.assertIsNone(seq.observe('A', 47))  # never move the high-water mark back
        with self.assertRaises(ProtocolError):
            seq.observe('B', 0)

    def test_methods_encode_go_vectors(self):
        class Sink:
            def __init__(self): self.sent = []
            def sendall(self, data): self.sent.append(data)
            def close(self): pass
        c = Client(Sink())
        c.new('N', 'TEST', pb.BUY, 10, 12345, client_order_id='client-N')
        c.cancel('N', 'TEST', 'cancel-N')
        c.replace('N', 'R', 'TEST', 'replace-N', new_price=12346, new_quantity=7, new_display_quantity=0)
        c.mass_cancel('mass-N', symbol='TEST')
        c.subscribe('TEST')
        for frame, name in zip(c._socket.sent, ['new', 'cancel', 'replace-zero', 'mass-cancel', 'subscribe']):
            self.assertEqual(frame, (GOLDEN / f'{name}.bin').read_bytes())

    def test_dev_jwt_claims_and_signature_local_only(self):
        with patch('fxp_client.client.time.time', return_value=1700000000), patch('fxp_client.client.secrets.token_hex', return_value='test-jti'):
            token = dev_token('local-trader', secret='a' * 32, ttl=60, issuer='test', audience='gateway', kid='key')
        self.assertEqual(token, (GOLDEN / "dev-token.txt").read_text().strip())
        head, payload, sig = token.split('.')
        decode = lambda s: json.loads(base64.urlsafe_b64decode(s + '=' * (-len(s) % 4)))
        self.assertEqual(decode(head), {'alg': 'HS256', 'typ': 'JWT', 'kid': 'key'})
        self.assertEqual(decode(payload), {'sub': 'local-trader', 'iss': 'test', 'aud': 'gateway', 'iat': 1700000000, 'nbf': 1700000000, 'exp': 1700000060, 'jti': 'test-jti'})
        expected = base64.urlsafe_b64encode(hmac.new(b'a' * 32, f'{head}.{payload}'.encode(), hashlib.sha256).digest()).rstrip(b'=').decode()
        self.assertEqual(sig, expected)
        for kwargs in ({'secret': ''}, {'secret': 'short'}, {'secret': 'a' * 32, 'ttl': 0}, {'secret': 'a' * 32, 'ttl': 86401}):
            with self.assertRaises(ValueError): dev_token('x', **kwargs)

    def test_receive_preserves_gap_event_and_closes_on_corrupt_frame(self):
        a, b = socket.socketpair()
        try:
            c = Client(a)
            for n in (1, 3):
                b.sendall(encode_frame(pb.FXPMessage(trade_capture_report=pb.TradeCaptureReport(symbol='T', seq=n))))
            self.assertIsNone(c.recv().trade_gap)
            event = c.recv()
            self.assertEqual(event.message.trade_capture_report.seq, 3)
            self.assertEqual(event.trade_gap, TradeGap('T', 2, 3))
            b.sendall(b'\0\0\0\1\xff')
            with self.assertRaises(ProtocolError): c.recv()
            self.assertEqual(a.fileno(), -1)
        finally:
            a.close(); b.close()
