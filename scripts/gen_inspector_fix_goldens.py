#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Independent FIX field, BodyLength and checksum construction; no bridge imports."""
import json
from pathlib import Path

FIX_TIME = "20261026-12:34:56.789"
CASES = [
    ("limit", 1, 2, 1, 0, {}),
    ("stop-limit-gtd", 2, 4, 7, 5, {
        "stopPrice": "12400", "expireDate": "2026-12-31",
        "displayQuantity": 2, "stpGroup": "group-a", "account": "demo",
    }),
]
fixtures = []
for name, side, order_type, tif, stp, extra in CASES:
    # Numeric protobuf enums follow fxp.proto; FIX tags/values are specified here
    # independently rather than generated through the translator being tested.
    order = {
        "clientOrderId": name, "orderId": name, "symbol": "DEMO",
        "side": side, "quantity": 10, "orderType": order_type,
        "timeInForce": tif, "price": "12345",
        "timestamp": "2026-10-26T12:34:56.789Z", "stpMode": stp, **extra,
    }
    fields = [
        (35, "D"), (49, "CONSOLE"), (56, "FXPBRIDGE"), (34, "1"),
        (52, FIX_TIME), (11, name), (55, "DEMO"), (54, str(side)),
        (38, "10"), (40, str(order_type)), (59, {1: "0", 7: "6"}[tif]),
        (60, FIX_TIME), (44, "123.45"),
    ]
    if extra:
        fields += [
            (99, "124.00"), (1, "demo"), (432, "20261231"),
            (111, "2"), (2964, "99"), (2362, "group-a"),
        ]
    body = "".join(f"{tag}={value}\x01" for tag, value in fields)
    frame = f"8=FIX.4.4\x019={len(body.encode())}\x01" + body
    frame += f"10={sum(frame.encode()) % 256:03}\x01"
    fixtures.append({"name": name, "order": order, "fix": frame.replace("\x01", "|")})

path = Path(__file__).resolve().parent.parent / "fix_bridge/translator/testdata/order_preview.json"
path.write_text(json.dumps(fixtures, indent=2) + "\n")
