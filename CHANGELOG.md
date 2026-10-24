# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project has not
cut a release yet, so everything is under Unreleased.

## [Unreleased]

### Changed
- FIX codec rejects empty fields (SOH SOH), malformed `tag=value` pairs and
  any BeginString other than the configured one (default `FIX.4.4`).
- Benchmarks moved to `fxp_core/benches/`; `go_gateway/test_clients` split
  into `tcp_client` and `ws_client` so `go build ./...` succeeds.
- Removed unused Rust dependencies (`tokio-tungstenite`, `futures-util`,
  `url`, `rand`, `hex`); no wildcard versions remain.

### Added
- `go.work`, `Makefile`, `scripts/e2e.sh`, `rustfmt.toml`, `.golangci.yml`,
  `SECURITY.md`, `CONTRIBUTING.md`, SPDX headers.

### Removed
- Committed Windows binary `go_gateway/bench` and the dead, uncompiled
  `fxp_core/src/test.rs` / `integration_test.rs`.

### Fixed (earlier audit work)
- Medium: FXP schema v2.0 (explicit `*_UNSPECIFIED` enums), dependency and
  toolchain bumps, FIX codec/session hardening, README/Benchmark.md accuracy.
- High: resting-price execution, execution report fields, FIX session
  authentication/resend/gap handling, gateway limits.
- Critical: FIFO within a price level, `order_lookup` bookkeeping, order
  ownership checks, FIX BodyLength, single MsgSeqNum counter.
