#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Project primary R4 evidence without pooling, rounding or executing workloads."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
NAMES = ("open-c1-sleep", "open-c1-busy-spin", "open-c1-hybrid", "closed-c1", "open-c10")


def export():
    scenarios = []
    for name in NAMES:
        source = Path("benchmarks/round4") / (name + ".json.gz")
        raw = (ROOT / source).read_bytes()
        data = json.loads(gzip.decompress(raw))
        if data["schema_version"] != 1 or data.get("error") or data["config"]["gateway"] != "127.0.0.1:8082" or data["config"]["tls"]["enabled"]:
            raise ValueError(f"Unsupported source population: {source}")
        scenarios.append(dict(
            id=name, source=str(source), source_sha256=hashlib.sha256(raw).hexdigest(),
            git_sha=data["environment"]["revision"], created_utc=data["created_utc"],
            network="loopback", protocol="FXP", config=data["config"],
            environment=data["environment"],
            runs=[{k: {a: b for a, b in v.items() if a != "hdr"} if isinstance(v, dict)
                   else v for k, v in run.items()} for run in data["runs"]],
            summary=data["summary"],
        ))
    return json.dumps(dict(schema_version=1, profile="/api/bench/profile-r4",
                           scenarios=scenarios), indent=2) + "\n"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    target = ROOT / "benchmarks/console.json"
    result = export()
    if args.check:
        if target.read_text() != result:
            raise SystemExit("Console evidence is stale; run scripts/export_console_bench.py")
        print("Console projection matches primary R4 sources and hashes")
    else:
        target.write_text(result)
