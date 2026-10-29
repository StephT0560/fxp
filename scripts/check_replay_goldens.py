#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Independent stdlib validation of checked-in log/trace encodings and SHA-256 streams."""
import hashlib
import json
from pathlib import Path
import struct
import zlib

ROOT = Path(__file__).resolve().parents[1]


def fields(data):
    def varint(offset):
        value = 0
        for shift in range(0, 70, 7):
            byte = data[offset]
            offset += 1
            value |= (byte & 127) << shift
            if byte < 128:
                return value, offset
        raise AssertionError("invalid varint")
    result = {}
    offset = 0
    while offset < len(data):
        tag, offset = varint(offset)
        number, wire = tag >> 3, tag & 7
        if wire == 0:
            value, offset = varint(offset)
        elif wire == 2:
            size, offset = varint(offset)
            value = data[offset:offset + size]
            assert len(value) == size
            offset += size
        else:
            raise AssertionError(f"unexpected wire type {wire}")
        result[number] = value
    return result


def validate(path):
    manifest = json.loads((path / "manifest.json").read_text())
    expected = json.loads((path / "expected.json").read_text())
    log = (path / "commands.log").read_bytes()
    trace = (path / "events.jsonl").read_bytes()
    assert manifest["commands_sha256"] == hashlib.sha256(log).hexdigest()
    assert manifest["trace_sha256"] == hashlib.sha256(trace).hexdigest()
    assert manifest["symbol_config_sha256"] == hashlib.sha256((path / "symbols.csv").read_bytes()).hexdigest()
    assert manifest["proto_schema_sha256"] == hashlib.sha256((ROOT / "fxp_core/proto/fxp.proto").read_bytes()).hexdigest()
    assert log[:8] == b"FXPEVL01"
    offset = 8
    commands = {}
    while offset < len(log):
        size = struct.unpack_from(">I", log, offset)[0]
        raw = log[offset + 4:offset + 4 + size]
        crc = struct.unpack_from(">I", log, offset + 4 + size)[0]
        assert crc == zlib.crc32(log[offset:offset + 4 + size])
        record = fields(raw)
        if record.get(1, 0):
            commands[record[1]] = record
        offset += size + 8
    assert offset == len(log)
    assert list(commands) == list(range(1, len(commands) + 1))
    rows = [json.loads(line) for line in trace.splitlines()]
    domain = b"fxp-business-v1\0"
    global_bytes = bytearray(domain)
    streams = {r[4].decode(): bytearray(domain) for r in commands.values()}
    counts = {symbol: 0 for symbol in streams}
    total = 0
    seen = []
    fills = []
    for row in rows:
        seq = row["global_seq"]
        assert row["symbol"] == commands[seq][4].decode()
        seconds = commands[seq].get(2, 0)
        if seconds >= 1 << 63:
            seconds -= 1 << 64
        assert row["command_time"] == {"seconds": seconds, "nanos": commands[seq].get(3, 0)}
        if row["kind"] == "command_received":
            seen.append(seq)
            assert row["data"]["protobuf_hex"] == commands[seq].get(6, b"").hex()
            data = row["data"]
            kind = commands[seq][5]
            assert data["kind"] == {1: "new", 2: "cancel", 3: "replace", 4: "tick", 5: "subscribe", 6: "mass_cancel", 7: "session_control"}[kind]
            payload = fields(commands[seq].get(6, b""))
            strings = {
                1: {"order_id": 2, "owner": 3, "expire_date": 13, "account": 14, "client_order_id": 15, "stp_group": 17},
                2: {"order_id": 2, "client_order_id": 3, "owner": 4},
                3: {"order_id": 2, "new_order_id": 3, "client_order_id": 4, "owner": 5},
                6: {"owner": 2, "client_order_id": 5},
            }
            integers = {
                1: {"price": (6, 64), "side": (7, 32), "quantity": (8, 32), "order_type": (9, 32), "time_in_force": (10, 32), "stop_price": (12, 64), "stp_mode": (16, 32), "display_quantity": (18, 32)},
                3: {"new_price": (8, 64), "new_quantity": (9, 32), "new_time_in_force": (10, 32), "new_stop_price": (12, 64)},
                6: {"side": (4, 32), "reason": (6, 32)},
                7: {"opening_call_seconds": (3, 64)},
            }
            for key, tag in strings.get(kind, {}).items():
                assert data[key] == payload.get(tag, b"").decode()
            for key, (tag, bits) in integers.get(kind, {}).items():
                value = payload.get(tag, 0) & ((1 << bits) - 1)
                if value >= 1 << (bits - 1):
                    value -= 1 << bits
                assert data[key] == value
            if kind == 3:
                if 13 not in payload:
                    assert data["new_display_quantity"] is None
                else:
                    value = payload[13] & 0xffffffff
                    if value >= 1 << 31:
                        value -= 1 << 32
                    assert data["new_display_quantity"] == value

        if not row["canonical_hex"]:
            continue
        frame = bytes.fromhex(row["canonical_hex"])
        assert struct.unpack_from(">Q", frame, 1)[0] == len(frame) - 9
        payload = fields(frame[9:])
        time_field = {1: 9, 2: 8, 3: 3, 4: 8}[frame[0]]
        timestamp = fields(payload[time_field])
        assert row["data"]["business_time"] == {"seconds": timestamp.get(1, 0), "nanos": timestamp.get(2, 0)}
        if frame[0] == 1:
            assert payload[2].decode() == row["data"]["execution_id"]
            assert payload[3].decode() == row["order_id"]
            assert payload.get(7, 0) == row["data"]["price"]
            assert payload.get(8, 0) == row["data"]["quantity"]
            assert payload[4].decode() == row["symbol"]
            if row["kind"] == "fill":
                fills.append(row)
        elif frame[0] == 2:
            assert payload[5].decode() == row["symbol"]
            assert payload[6] == row["data"]["price"]
            assert payload[7] == row["data"]["quantity"]
            pair = fills[-2:]
            assert len(pair) == 2
            assert pair[0]["data"]["counterparty"] == pair[1]["order_id"]
            assert pair[1]["data"]["counterparty"] == pair[0]["order_id"]
            assert all(r["data"]["quantity"] == row["data"]["quantity"] for r in pair)
        elif frame[0] in [3, 4]:
            assert payload[1].decode() == row["symbol"]
        else:
            raise AssertionError("unknown canonical tag")
        global_bytes.extend(frame)
        streams[row["symbol"]].extend(frame)
        counts[row["symbol"]] += 1
        total += 1
    assert seen == list(commands)
    assert expected["global"] == {"count": total, "sha256": hashlib.sha256(global_bytes).hexdigest()}
    assert expected["symbols"] == {s: {"count": counts[s], "sha256": hashlib.sha256(b).hexdigest()} for s, b in streams.items()}
    print(f"PASS {path.name}: {len(commands)} commands, {total} business events")


if __name__ == "__main__":
    for bundle in sorted((ROOT / "fxp_core/tests/bundles").iterdir()):
        if bundle.is_dir():
            validate(bundle)
