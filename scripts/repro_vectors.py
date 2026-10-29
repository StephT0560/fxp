#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Independent canonical business-event vectors: Python stdlib, no Rust/protobuf dependency."""
import hashlib
import json
import struct
import sys


def varint(n):
    n &= (1 << 64) - 1
    out = bytearray()
    while n > 127:
        out.append((n & 127) | 128)
        n >>= 7
    out.append(n)
    return bytes(out)


def integer(field, n):
    return varint(field << 3) + varint(n) if n else b""


def blob(field, value):
    if isinstance(value, str):
        value = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(value)) + value if value else b""


def frame(tag, payload):
    return bytes([tag]) + struct.pack(">Q", len(payload)) + payload


def vectors():
    ts = integer(1, 42) + integer(2, 123)
    er = b"".join([blob(2, "X1"), blob(3, "Oé"), blob(4, "A"), integer(5, 1),
                   integer(6, 1), integer(7, 123), integer(8, 2), blob(9, ts),
                   integer(10, 8), integer(11, 123), integer(12, 2), blob(13, "acct"),
                   blob(14, "c"), integer(17, 1), integer(18, 10), integer(20, 3)])
    trade = b"".join([blob(2, "T1"), blob(5, "A"), integer(6, 123), integer(7, 2),
                      blob(8, ts), integer(9, 1), integer(10, 1)])
    phase = blob(1, "A") + integer(2, 2) + blob(3, ts)
    auction = b"".join([blob(1, "A"), integer(2, 1), integer(3, 123), integer(4, 5),
                        integer(5, 2), integer(6, 2), integer(7, 120), blob(8, ts)])
    frames = [frame(i + 1, p) for i, p in enumerate([er, trade, phase, auction])]
    result = {}
    for i, data in enumerate(frames):
        result[f"event_{i}"] = data.hex()
        result[f"sha256_{i}"] = hashlib.sha256(data).hexdigest()
    # State of a fresh continuous-session engine A at injected time 42.000000123.
    # Independent fixed-width encoding, not a call into the Rust dump function.
    def u64(n):
        return struct.pack(">Q", n)

    def text(s):
        b = s.encode("utf-8")
        return u64(len(b)) + b

    empty_engine = (text("fxp-state-v1") + text("A") + u64(0) * 12
                    + u64(3) + u64(0) * 3 + struct.pack(">qq", 42, 123)
                    + u64(0) + u64(1) + struct.pack(">qQ", 0, 0) + bytes(16)
                    + text("") + text("none") + struct.pack(">QqQQQ", 2, 1, 1, 1, 2147483647)
                    + u64(0) * 3)
    result["state_empty_engine"] = empty_engine.hex()
    result["state_empty_engine_sha256"] = hashlib.sha256(empty_engine).hexdigest()
    result["global_sha256"] = hashlib.sha256(b"fxp-business-v1\0" + b"".join(frames)).hexdigest()
    return result


def metadata_vector():
    return {"seed": 7, "unicode": "é😀", "escapes": "quote\" slash\\ newline\n control\x01",
            "types": [True, False, None], "numbers": [0, -12, 1.25, 1e40],
            "nested": {"array": ["x", {"a": 1}]}}


if __name__ == "__main__":
    if "--metadata" in sys.argv:
        print(json.dumps(metadata_vector(), ensure_ascii=True))
    elif "--check-metadata" in sys.argv:
        assert json.load(sys.stdin) == metadata_vector()
    elif "--lines" in sys.argv:
        for key, value in sorted(vectors().items()):
            print(f"{key}={value}")
    else:
        print(json.dumps(vectors(), sort_keys=True))
