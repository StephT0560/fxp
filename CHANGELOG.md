# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The 0.1.0 section is prepared from repository history through the round-4 CI
work; it is not a claim that a tag or release has been created. Set its release
date when the reviewed main commit is tagged.

## [Unreleased]

## [0.1.0] - Pending first release

### Added
- Rust matching core, Go TCP/WebSocket gateway, FIX 4.4 bridge and Python test
  client; protobuf FXP protocol, Go workspace, Docker/Compose and CI tooling.
- Limit, market, stop/stop-limit, cancel/replace, time-in-force and expiry;
  per-print stop cascades, opening/closing call auctions, daily UTC schedules,
  indicative feeds and additive FXP v2.1 auction/session messages.
- Opt-in write-ahead input-log persistence (`FXP_EVENT_LOG`), checksum tail repair,
  deterministic replay, downtime catch-up and crash/restart checks.
- Owner/group self-trade prevention, owner-scoped mass cancel and opt-in
  cancel-on-disconnect (additive FXP v2.2), owner-scoped order/mass status (v2.3),
  and FIFO iceberg reserve replenishment with displayed-only depth (v2.4).
- Per-symbol price precision, tick/lot/min/max quantity rules and replay checks.
- FIX durable sequence/message storage, resend/gap recovery, pinned FIX44 XML
  dictionary validation, daily/weekly schedules and archived session resets.
- Reloadable TLS 1.2+/mTLS on gateway and FIX listeners, verified upstream
  clients, and optional constant-time core token handshake before frame parsing.
  TLS and core authentication remain opt-in; default configuration is unchanged.
- Bounded slow-consumer queues, symbol admission limits, and graceful shutdown
  across core, gateway and bridge; operations and security guidance.
- Intended-time open-loop and ACK-controlled closed-loop benchmarks, warmup,
  repeated HDR/spread reports, reusable FIX encoding, wire-integrity tests and
  retained raw measurements; nightly/manual non-gating benchmark artifacts.
- Static Linux amd64/arm64 release builds, ELF verification, SHA256SUMS,
  CycloneDX source/binary inventories and draft-only tag releases. Manual builds
  create reviewable artifacts without creating a release.
- QuickFIX acceptance gate: 28 of 59 definitions pass strictly, with 31
  documented allow-listed cases. TLS, persistence and order-type E2E coverage.
- Stable `ci-success` required check over every CI job, including both Go matrix
  legs; failed, cancelled or skipped dependencies fail the gate (round 4 Low #3).
- Dependabot Cargo/Go minor/patch grouping, Actions grouping, five open PRs per
  ecosystem and dependency labels. Auto-merge remains disabled. Manual main
  ruleset, branch hygiene and first-release checklists (round 4 Low #3 / §6).

### Changed
- JWTs require HS256 and issuer/audience claims as well as subject/expiry/issued
  time, honor `nbf`, and support key rotation and optional `jti` replay checks.
  Existing external token issuers must supply `iss`/`aud` (defaults `fxp-dev` /
  `fxp-gateway`). WebSocket upgrades are same-origin by default with an allowlist.
- FXP v2.0 introduced explicit unspecified enum sentinels; later v2.1–v2.4 schema
  additions are additive. Unknown enum values are rejected.
- Core binds to loopback; gateway limits frames, connections and request rate.
  TCP_NODELAY, one reader per pooled core connection and flushed terminal replies
  improve transport behavior. Logging uses tracing/slog with configurable levels.
- Corrected benchmark methodology supersedes historical latency claims: retained
  results show reusable FIX encoding faster than FXP for that fixture/run.

### Fixed
- FIFO price-level priority, order lookup bookkeeping, resting-price execution,
  cumulative report fields, FOK arithmetic and order ownership isolation.
- FIX BodyLength/checksum handling, a single session-owned outbound sequence
  counter, authenticated Logon, malformed field rejection and session recovery.
- Auction/expiry transitions, per-print stop election, iceberg/STP replace totals,
  reserve caps, active-field tick validation and deterministic replay edges.
- FIX dictionary lexical validation, bounded status batches, recycled status-owner
  isolation and preservation of durable messages when reset archiving fails.
- Benchmark deadline refresh, blocked sender cancellation and joined error
  diagnostics; clippy and license compatibility after dependency upgrades.

- Correct the iceberg E2E fixture's assumption that requeued maker reports retain
  inbox order; expected cumulative fill values and taker-priority checks remain.

### Removed
- Committed Windows benchmark executable and dead uncompiled Rust test files;
  unused Rust dependencies and wildcard version requirements. The old executable
  remains in Git history; no history rewrite was performed.

### Known limitations
- Pegged orders remain design-only. QuickFIX allow-listed differences and routing
  gaps remain; this release is not a counterparty certification claim.
- Production operators must explicitly configure TLS/core auth. Main's required
  check ruleset and stale branch cleanup require maintainer action.
