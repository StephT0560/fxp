# Changelog

## Unreleased

- Complete header/trailer ordering and FIX decimal lexical checks; distinguish missing/empty CompIDs from authenticated identity mismatches (#2).

- Additive protocol v2.3 owner-scoped order status (FIX H/AF), terminal-history snapshots and independent batched query routing (#2).

- Pinned FIX44 XML dictionary validation with reject diagnostics, group validation and optional strict user-tag validation (#2).

- Optional FIX daily/weekly schedules, timezone-aware boundaries, archived durable resets and reset-on-logon/logout/disconnect policies (#2).

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project has not
cut a release yet, so everything is under Unreleased.

## [Unreleased]

### Added
- Bounded core and gateway client output queues isolate slow consumers; critical
  output overflow disconnects the client while market-data overflow is dropped.
- Core symbol admission controls via `FXP_SYMBOLS` and `FXP_MAX_SYMBOLS`, with
  graceful SIGINT/SIGTERM handling across core, gateway, and FIX bridge.
- Operations guidance for backpressure, symbol admission, and graceful shutdown.
- Self-trade prevention (FXP v2.2, additive): per-order `stp_mode`/`stp_group`
  with cancel-newest, cancel-oldest, cancel-both and decrement-and-cancel,
  scoped to the owner or a per-trader STP group; engine default
  `FXP_STP_DEFAULT` (default `none`, unchanged behavior). Prevented matches
  print no trade and cancel with text `self-trade prevention (<mode>)`.
  FIX tags 2362/2964. Explicit STP on auction-only orders is rejected;
  auction uncrossing ignores STP (#1).
- Owner-scoped `OrderMassCancelRequest`/`OrderMassCancelReport` (optional
  symbol and side filters, parked stops included); FIX `35=q` (530=7 all,
  530=1 by symbol) answered with `35=r` (531/532/533) (#1).
- Cancel-on-disconnect per session (`Logon.cancel_on_disconnect`, FIX Logon
  `8013=Y`; default off): any disconnect mass-cancels the trader's orders,
  and the bridge stores the canceled ERs for resend (#1).
- Opt-in core input-log persistence with synchronous write-ahead fsync, checksum
  tail repair, deterministic replay and downtime catch-up, enabled by
  `FXP_EVENT_LOG`; includes a core SIGKILL/restart E2E check (#8).
- Engine auction phases and full daily UTC schedules: non-matching call books,
  held AT_CLOSE orders, opening/closing uncrosses, auction-flagged fills,
  indicative/session feeds, and opening-only stop election (#8).
- Pure deterministic auction uncrossing/allocation and additive FXP v2.1:
  trading phases, session status, indicative auction data and an execution
  report auction flag. Session/auction frames follow symbol subscriptions;
  the FIX bridge ignores them without consuming sequence numbers (#8).
- FIX bridge required-field dictionary for NewOrderSingle, OrderCancelRequest,
  OrderCancelReplaceRequest and MarketDataRequest; a message missing one gets
  `Reject(35=3)` with `373=1`, `371=<tag>`, `372=<MsgType>` (#9).
- Multi-stage `Dockerfile`, `docker-compose.yml`, GitHub issue and PR templates.
- `RUST_LOG` (core) and `FXP_LOG_LEVEL` (gateway, bridge) log-level control (#10).

### Changed
- Gateway authentication failures and pre-authentication errors are flushed
  before the TCP or WebSocket connection closes. The FIX bridge synthesizes a
  unique tag 17 for ExecutionReports without an execution ID, including
  gateway-generated rejects; non-empty core execution IDs are unchanged.
- Core shutdown drains connected-client writers without waiting for
  journal-held senders, and FIX bridge Logout requests share one shutdown
  deadline before remaining sessions are closed.
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
