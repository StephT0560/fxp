# Replay fixtures

Synthetic small bundles generated from fixed commands and injected UTC clocks.
The manifest's all-zero Git SHA and zero start/end provenance times are explicit
fixture placeholders, not a claim that these were production runs. Commands,
canonical event bytes, digests and final-state expectations are real engine output.

Each directory contains unchanged-format `commands.log`, the recorded event trace,
manifest, expected hashes, and reviewed text/JSON timeline and explanation snapshots.
The payloads include no credentials. Counterparties in display snapshots are
pseudonymized by default; the raw recorded trace retains actual IDs for verification.

Regenerate intentionally, then review every diff:

```sh
FXP_UPDATE_GOLDENS=1 cargo test --manifest-path fxp_core/Cargo.toml --lib regenerate_cli_goldens
python3 scripts/check_replay_goldens.py
cargo test --manifest-path fxp_core/Cargo.toml --test replay_cli
```

Normal tests never rewrite fixtures. The Python checker independently decodes the
command protobuf frames, checks CRCs and hashes, parses trace JSON, checks canonical
business payload fields and validates global/per-symbol SHA-256 streams. It does
not import or execute the Rust codec.
