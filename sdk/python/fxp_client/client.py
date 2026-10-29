# SPDX-License-Identifier: MIT
"""Blocking single-reader client; four-byte big-endian length + FXPMessage.

No automatic reconnect/resend: resending an order after a disconnect can duplicate
intent. Callers own deadlines, reconciliation and recovery. Receive/framing errors
close the connection, since a partially consumed frame cannot safely be retried.
"""
from __future__ import annotations

import base64
from dataclasses import dataclass
import hashlib
import hmac
import json
import os
import secrets
import socket
import ssl
import struct
import time
from typing import Iterator

from google.protobuf.message import DecodeError
from . import fxp_pb2 as pb

MAX_FRAME = 65536  # gateway default; constructor accepts a matching deployment limit


class ProtocolError(ValueError):
    """Invalid frame or protobuf payload; connection is no longer usable."""


def _exact(sock: socket.socket, n: int, *, boundary: bool = False) -> bytes:
    data = bytearray()
    while len(data) < n:
        part = sock.recv(n - len(data))
        if not part:
            if boundary and not data:
                raise EOFError("gateway disconnected")
            raise ProtocolError("truncated gateway frame")
        data.extend(part)
    return bytes(data)


def read_frame(sock: socket.socket, max_frame: int = MAX_FRAME) -> bytes:
    size, = struct.unpack('!I', _exact(sock, 4, boundary=True))
    if not 0 < size <= max_frame:
        raise ProtocolError(f"frame length {size} outside 1..{max_frame}")
    return _exact(sock, size)


def encode_frame(message: pb.FXPMessage, max_frame: int = MAX_FRAME) -> bytes:
    if message.WhichOneof('payload') is None:
        raise ProtocolError('FXPMessage has no payload')
    data = message.SerializeToString(deterministic=True)
    if not 0 < len(data) <= max_frame:
        raise ProtocolError(f"frame length {len(data)} outside 1..{max_frame}")
    return struct.pack('!I', len(data)) + data


@dataclass(frozen=True)
class TradeGap:
    symbol: str
    expected: int
    actual: int


class TradeSequences:
    """Per-connection high water marks. First print establishes each baseline.

    Gaps, duplicates and regressions are reported; they aren't repaired. No claim
    is made about missed trades before subscription or across a core restart.
    """
    def __init__(self):
        self._last: dict[str, int] = {}

    def observe(self, symbol: str, seq: int) -> TradeGap | None:
        if not symbol or seq <= 0:
            raise ProtocolError('trade requires symbol and positive sequence')
        previous = self._last.get(symbol)
        self._last[symbol] = max(seq, previous or 0)
        return TradeGap(symbol, previous + 1, seq) if previous is not None and seq != previous + 1 else None


@dataclass(frozen=True)
class Event:
    message: pb.FXPMessage
    trade_gap: TradeGap | None = None

    @property
    def kind(self) -> str:
        return self.message.WhichOneof('payload')


class Client:
    """One connection, one reader; no concurrent method calls/thread safety."""
    def __init__(self, sock: socket.socket, *, max_frame: int = MAX_FRAME):
        if max_frame <= 0:
            raise ValueError('max_frame must be positive')
        self._socket = sock
        self.max_frame = max_frame
        self.trade_sequences = TradeSequences()

    @classmethod
    def connect(cls, host: str, port: int, token: str, *, timeout: float = 10,
                tls: ssl.SSLContext | None = None, server_hostname: str | None = None,
                cancel_on_disconnect: bool = False, max_frame: int = MAX_FRAME) -> Client:
        """Connect and authenticate; TLS verification follows the supplied context."""
        sock = socket.create_connection((host, port), timeout=timeout)
        try:
            if tls is not None:
                sock = tls.wrap_socket(sock, server_hostname=server_hostname or host)
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            client = cls(sock, max_frame=max_frame)
            client.send(pb.FXPMessage(logon=pb.Logon(token=token, cancel_on_disconnect=cancel_on_disconnect)))
            ack = client.recv().message
            if ack.WhichOneof('payload') != 'logon_ack':
                raise ProtocolError('expected LogonAck')
            if not ack.logon_ack.accepted:
                raise PermissionError(ack.logon_ack.reject_reason)
            return client
        except BaseException:
            sock.close()
            raise

    def close(self) -> None:
        self._socket.close()

    def __enter__(self) -> Client:
        return self

    def __exit__(self, *_):
        self.close()

    def send(self, message: pb.FXPMessage) -> None:
        frame = encode_frame(message, self.max_frame)
        try:
            self._socket.sendall(frame)
        except OSError:
            self.close()
            raise

    def recv(self) -> Event:
        try:
            raw = read_frame(self._socket, self.max_frame)
            message = pb.FXPMessage.FromString(raw)
            if message.WhichOneof('payload') is None:
                raise ProtocolError('FXPMessage has no known payload; check schema version')
            gap = None
            if message.HasField('trade_capture_report'):
                trade = message.trade_capture_report
                gap = self.trade_sequences.observe(trade.symbol, trade.seq)
            return Event(message, gap)
        except DecodeError as exc:
            self.close()
            raise ProtocolError('invalid FXP protobuf') from exc
        except (OSError, EOFError, ProtocolError):
            self.close()
            raise

    def events(self) -> Iterator[Event]:
        """Yield every message, including error reports; clean EOF ends iteration."""
        while True:
            try:
                yield self.recv()
            except EOFError:
                return

    def new(self, order_id: str, symbol: str, side: int, quantity: int, price: int = 0,
            *, order_type: int = pb.LIMIT, time_in_force: int = pb.GTC, **fields) -> None:
        self.send(pb.FXPMessage(new_order=pb.NewOrderSingle(order_id=order_id, symbol=symbol,
                  side=side, quantity=quantity, price=price, order_type=order_type,
                  time_in_force=time_in_force, **fields)))

    def cancel(self, order_id: str, symbol: str, client_cancel_id: str) -> None:
        self.send(pb.FXPMessage(order_cancel=pb.OrderCancelRequest(order_id=order_id, symbol=symbol,
                  client_cancel_id=client_cancel_id)))

    def replace(self, order_id: str, new_order_id: str, symbol: str, client_replace_id: str,
                *, new_price: int = 0, new_quantity: int = 0, new_display_quantity: int | None = None,
                **fields) -> None:
        """new_quantity is TOTAL quantity (filled + desired leaves); zero keeps it.

        new_display_quantity=None keeps the current display, 0 fully displays.
        Other generated protobuf fields are accepted through fields.
        """
        request = pb.OrderCancelReplaceRequest(order_id=order_id, new_order_id=new_order_id,
                  symbol=symbol, client_replace_id=client_replace_id, new_price=new_price,
                  new_quantity=new_quantity, **fields)
        if new_display_quantity is not None:
            request.new_display_quantity = new_display_quantity
        self.send(pb.FXPMessage(order_cancel_replace=request))

    def mass_cancel(self, client_request_id: str, *, symbol: str = '', side: int = 0) -> None:
        self.send(pb.FXPMessage(order_mass_cancel=pb.OrderMassCancelRequest(client_request_id=client_request_id,
                  symbol=symbol, side=side, reason=pb.MASS_CANCEL_REASON_REQUEST)))

    def subscribe(self, *symbols: str) -> None:
        if not symbols or any(not s for s in symbols):
            raise ValueError('at least one nonempty symbol required')
        self.send(pb.FXPMessage(market_data_request=pb.MarketDataRequest(symbols=symbols)))


def dev_token(subject: str, *, secret: str | None = None, ttl: int = 900,
              issuer: str | None = None, audience: str | None = None, kid: str | None = None) -> str:
    """LOCAL USE ONLY: mint HS256 JWT with stdlib; real bots receive an issuer token.

    Defaults use the same FXP_JWT_* variables as the gateway. No secret fallback,
    token printing or production identity-provider behavior is provided.
    """
    secret = os.environ.get('FXP_JWT_SECRET', '') if secret is None else secret
    if len(secret.encode()) < 32 or not subject or not 1 <= ttl <= 86400:
        raise ValueError('dev_token requires a >=32-byte secret, subject, and TTL 1..86400 seconds')
    header = {'alg': 'HS256', 'typ': 'JWT'}
    kid = os.environ.get('FXP_JWT_KID', '') if kid is None else kid
    if kid:
        header['kid'] = kid
    now = int(time.time())
    claims = {'sub': subject, 'iss': issuer or os.environ.get('FXP_JWT_ISSUER', 'fxp-dev'),
              'aud': audience or os.environ.get('FXP_JWT_AUDIENCE', 'fxp-gateway'),
              'iat': now, 'nbf': now, 'exp': now + ttl, 'jti': secrets.token_hex(16)}
    encode = lambda value: base64.urlsafe_b64encode(json.dumps(value, separators=(',', ':')).encode()).rstrip(b'=')
    data = encode(header) + b'.' + encode(claims)
    signature = base64.urlsafe_b64encode(hmac.new(secret.encode(), data, hashlib.sha256).digest()).rstrip(b'=')
    return (data + b'.' + signature).decode()
