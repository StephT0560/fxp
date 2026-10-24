# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project has not
cut a release yet, so everything is under Unreleased.

## [Unreleased]

### Added
- FIX bridge required-field dictionary for NewOrderSingle, OrderCancelRequest,
  OrderCancelReplaceRequest and MarketDataRequest; a message missing one gets
  `Reject(35=3)` with `373=1`, `371=<tag>`, `372=<MsgType>` (#9).
- Multi-stage `Dockerfile`, `docker-compose.yml`, GitHub issue and PR templates.
- `RUST_LOG` (core) and `FXP_LOG_LEVEL` (gateway, bridge) log-level control (#10).

### Changed
- Unsupported FIX application messages (e.g. OrderStatusRequest `35=H`) get
  `BusinessMessageReject(35=j)` with `380=3` and an explicit text, even while
  the gateway is disconnected (#9).
- Core logs through `tracing`, gateway and bridge through `log/slog`;
  per-order/per-fill/per-message events moved to debug (#10).
- CI clippy runs with `-D warnings` (#10).
- **Token format (breaking):** the gateway now requires JWT header
  `alg: HS256` and the claims `iss` (default `fxp-dev`) and `aud` (default
  `fxp-gateway`) in addition to `sub`/`exp`/`iat`, honours `nbf`, and accepts
  an optional `kid` that selects the current or previous key
  (`FXP_JWT_KID`, `FXP_JWT_PREVIOUS_KID`, `FXP_JWT_PREVIOUS_SECRET`). Tokens
  minted by other issuers must add `iss`/`aud`. The bridge, `devjwt`, the
  benchmark and the simulation now mint `iss`, `aud`, `nbf`, `jti` (and `kid`
  when set). Optional `jti` replay cache: `FXP_JWT_REPLAY_CACHE=on`.
- WebSocket upgrades are same-origin only by default; extra browser origins
  via `FXP_GW_WS_ALLOWED_ORIGINS`. Clients without an `Origin` header are
  unaffected.
- Sockets between core, gateway and bridge set `TCP_NODELAY`, removing the
  ~40 ms Nagle/delayed-ACK stall on back-to-back execution reports.
- Gateway pool: exactly one reader goroutine per pooled core connection
  across reconnects.
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
