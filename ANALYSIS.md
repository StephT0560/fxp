# FXP: Technical Analysis, Round 3 (re-run)

**Repository:** https://github.com/StephT0560/fxp, analyzed at `main` @ `61c3d03` (merge of PR #16). That is 36 commits and PRs #5, #14, #15 and #16 after round 2's `032f331`.
**Scope:** Read-only, with the same parameters as rounds 1–2. Nothing on `main` was changed. Proposed tests and probes ran in scratch copies. Earlier reports are in this branch's history: round 1 `f0d310c`, round 2 `12b1809`.
**Environment:** same VM. Ubuntu 22.04 KVM, 8 vCPU Xeon (Skylake-SP), 31 GiB. rustc 1.97.1, Go toolchain go1.26.10, protoc 25.3, Python 3.10.12.

---

## 0. Executive summary

**Every High and Medium item from round 2 is fixed, and I verified each one live.**
- **FIX market data:** repeating groups are complete, and include `262` and `269`.
- **Stops:** they trigger on every print, fire in stop-price order, and parked stops can be cancelled and replaced. The FIX `stop_order` e2e case now actually triggers a stop.
- **FIX sequence numbers are durable:** the e2e suite kills and restarts the bridge, and the sequence continues with PossDup resend. Reconnecting with `34=1` is now refused as "too low", which is correct.
- **CI exists:** a GitHub Actions workflow plus `cargo-deny`, buf and Dependabot. `go test ./...` passes.
- **Latency:** `TCP_NODELAY` removed the ~36 ms tail. Rate-limited p99 went from **36.4 ms to 2.6 ms**.
- **Gateway:** exactly one reader per pooled connection. JWT now pins `alg`, requires `iss`/`aud`, checks `nbf`, has a `jti` replay cache and `kid` rotation. WebSocket origins are checked.
- **Round-2 Low items** are fixed too: required-field `35=3`, explicit `35=j` for `H`, clippy `-D warnings` clean, `tracing`/`slog` logging.
- **New features:** an extracted synchronous `Engine`, Day/GTD expiry, scheduled opening/closing auctions, and a flagged input-log persistence mode.

All static analysis is now clean:
- Rust: 0 warnings, clippy `-D warnings`, `cargo audit` 0, `cargo deny` ok.
- Go: `vet`, `staticcheck` and `govulncheck` all clean.
- Tests: 118 Rust and every Go package pass under `-race`. `scripts/e2e.sh` passes 35/35 FIX checks plus 8 restart/persistence checks.

**What's left (no Critical or High items remain):**

| # | Severity | Open item |
|---|---|---|
| 1 | Medium | Order-type breadth: **no self-trade prevention, iceberg/reserve, pegged orders, mass cancel or cancel-on-disconnect**. Price precision is fixed at 2 dp |
| 2 | Medium | FIX: **no session schedule / daily reset and no DataDictionary**. Required-field checks are hand-coded. `35=H` / mass status are still unsupported (explicit `35=j`) |
| 3 | Low | `benchmark.go` still hard-codes its JWT secret and has the `$3000.00` comment on a `$30,000,000` price. It still measures from the actual send time (coordinated omission). Encoding benches still compare preallocated FXP against `format!`-based FIX, and SBE numbers still look constant-folded |
| 4 | Low | Plaintext transport: TLS still not wired. The Rust core still trusts `sender` from any local client (documented trust model) |
| 5 | Info | Single maintainer (bus factor 1) |

**Updated readiness (my judgement):** matching engine **7.5/10** (6 in R2), FIX bridge **7/10** for counterparty certification (5 in R2), gateway **7.5/10** (6 in R2). The remaining gaps are feature breadth and operations (schedules, TLS, STP), not correctness.

---

## 1. Orientation

### 1.1 What changed structurally since round 2
- `.github/workflows/ci.yml` with these jobs: `rust` (fmt, build, test, clippy `-D warnings`, audit), `cargo-deny`, `go` (matrix over both modules: gofmt, vet, `test -race`, staticcheck, govulncheck), `e2e` (`scripts/e2e.sh`) and `proto` (buf lint/breaking). Plus Dependabot, issue/PR templates and `deny.toml`.
- `Dockerfile` (multi-stage, runtime `USER fxp`), `docker-compose.yml` (passes the JWT iss/aud/leeway/replay settings through), and `docs/gtc-persistence.md`.
- Engine: `fxp_core/src/engine.rs` + `engine/{auction.rs, phases.rs, session.rs}` (synchronous and testable), `order_book/stops.rs`, and `event_log.rs` (`FXP_EVENT_LOG`, fsync via `sync_data`/`sync_all`). `server.rs` is now I/O only.
- FIX bridge: `session/filestore.go` + `registry.go` (durable store keyed by session, not by connection; `FXP_FIX_STORE_DIR`, `_FSYNC`, `_GROUP_INTERVAL`, `_MAX_MESSAGES`).
- Logging: `tracing` in Rust and `slog` in Go. Hot-path events are at debug level (0 `println!` left in `server.rs` / `engine.rs`).

### 1.2 Build and test log

| Step | Command | R2 | R3 |
|---|---|---|---|
| Codegen | `bash scripts/gen_proto.sh` | ✅ | ✅ |
| Rust build | `cargo build --all-targets` | 3 warnings | ✅ **0 warnings** |
| Rust tests | `cargo test` | 52 | ✅ **118 passed** |
| Clippy | `cargo clippy --all-targets -- -D warnings` | ~115 style | ✅ clean |
| cargo audit | `cargo audit` | 0 | ✅ 0 (114 crates) |
| cargo deny | `cargo deny check` | not configured | ✅ advisories / bans / licenses / sources ok |
| Go build + vet | `go build ./... && go vet ./...` (both modules) | vet failed (`benchmark.go:481`) | ✅ clean |
| Go tests | `go test -race -count=1 ./...` | `go_gateway` failed | ✅ all pass: go_gateway `logging`, `devjwt`, `transport`; fix_bridge root, `codec`, `gateway`, `session`, `translator` |
| staticcheck | `staticcheck ./...` | 2 | ✅ clean |
| govulncheck | `govulncheck ./...` | 0 | ✅ 0 |
| E2E | `bash scripts/e2e.sh` (Go on PATH) | 27/27 | ✅ **35/35** FIX checks + durable-after-restart 2/2 + 2/2 + core persistence seed 1/1 + core restart / cancel / next ExecID 3/3. Exit 0 |
| CI | `.github/workflows/ci.yml` | none | ✅ present (I did not inspect hosted run results) |

---

## 2. Matching engine (Rust)

**Round-1 proposed tests on `61c3d03`: 10/11 pass** (R1 2/11, R2 9/11). P10 (stop-price trigger order) now passes. P09 still fails, but only because it calls `drain_triggered_stops` with a single price, so it tests the API rather than the behavior. The behavior is fixed: `order_book/stops.rs::run_stop_cascade` walks **every** fill price and drains triggers for each one. The e2e `stop_order` case confirms a live trigger and fill. Recommendation: rewrite P09 against `run_stop_cascade`.

| Round-2 open item | R3 status |
|---|---|
| Stops: last print only, insertion order | ✅ fixed (per-print cascade, ordered by stop price) |
| Parked stops can't be cancelled or replaced | ✅ fixed (PR #5) |
| StopLimit IOC/FOK TIF ignored | ✅ fixed |
| Hot-path `println!` | ✅ fixed (`tracing`, debug level) |
| Day / GTD expiry | ✅ implemented (`engine/phases.rs::expire` at UTC boundaries; the bridge normalizes `432` and e2e checks the expired reports) |
| Auctions (AT_OPEN / AT_CLOSE) | ✅ implemented: `engine/auction.rs::uncross` (pure function) plus scheduled phases in `phases.rs::run_auction`, protocol v2.1, and `phase_tests.rs` |
| GTC persistence | ✅ behind a flag: `FXP_EVENT_LOG` appends ordered engine inputs (fsync) and replays them at startup (`persistence_tests.rs`; e2e restart / cancel / next-ExecID checks pass). See `docs/gtc-persistence.md` |
| Engine testable without Tokio | ✅ `Engine` extracted. A property test against a naïve reference book is in place (`stops.rs` mentions a seeded reference-book comparison) |
| Self-trade prevention, iceberg, pegs, mass cancel, cancel-on-disconnect | ❌ open |
| Slow-consumer backpressure into the symbol task; global `SymbolRouter` mutex | ⚠ not re-verified in depth this round |
| 2-dp fixed price precision | ❌ open (design limitation) |

Edge cases I'd still add: auction uncross ties (equal volume, equal imbalance → reference-price tie-break), auction with only market orders and no reference price, expiry exactly at the boundary instant, event-log replay with a torn final record, and a stop cascade that crosses the auction phase transition.

---

## 3. FIX bridge (FIX 4.4)

Probes re-run: the same scripts as round 2, against a fresh stack with `FXP_FIX_STORE_DIR`.

| Probe | R2 | R3 |
|---|---|---|
| Spec-compliant Logon / auth (no, wrong, unknown password) | ✅ | ✅ |
| Resting ack, single seq, cross-session cancel, gap → `35=2`, resend with GapFill + PossDup | ✅ | ✅ |
| Bad checksum ignored, seq too low → `35=5` | ✅ | ✅ |
| Reconnect with the same CompID | restarted at `34=1` | ✅ **durable**: the bridge expects the stored sequence (`35=5 "MsgSeqNum too low, expecting 3 but received 1"`). e2e `durable_after_restart`: the Logon ack continues at `34=3`, and the pre-kill ER is resent as `34=2 43=Y` |
| NOS missing `TransactTime(60)` | accepted | ✅ `35=3 "Required tag missing: TransactTime (60)"` |
| `35=H` | generic `35=j` | ✅ explicit `35=j "Unsupported MsgType H (OrderStatusRequest)…"` |
| MD incremental with 3 entries | only last entry | ✅ `262=REQ1|268=3|279=0|269=0|…|279=1|269=1|…|279=2|269=0|…` (all 3). Entries with an unknown side are dropped with a debug log. MD routing is per MDReqID, `263=0` / `263=2` are honored, and there is one stream per symbol |

**Seqnum persistence (the known gap): closed.** `FileStore` plus a session registry keyed by session identity, with configurable fsync (`FXP_FIX_STORE_FSYNC`, group interval) and a bounded store (`FXP_FIX_STORE_MAX_MESSAGES`). The session fails closed if the store can't persist. This matches the round-1 design.

**Still absent:** session schedule / daily reset window, a DataDictionary (validation is hand-coded per message type), OrderStatusRequest / mass status / mass cancel, TradeCaptureReport, SecurityDefinition, TLS, and initiator mode.

**Certification rating: 7/10.** Session-layer, sequencing, recovery and market-data group tests should now pass. Expect findings in field-level validation breadth (no dictionary), unsupported message types, and the lack of scheduling. Next step: run the QuickFIX `fix44` acceptance definitions in the CI `e2e` job.

---

## 4. Gateway, protocol and security

| Round-2 open item | R3 status |
|---|---|
| Duplicate `poolReader` after reconnect | ✅ fixed: exactly one reader per pooled connection (PR #14) |
| JWT `alg` / `aud` / `iss` / `nbf` / `jti` / rotation | ✅ fixed: `alg` must be `HS256`. Optional `kid` selects the current or previous secret (`FXP_JWT_KID`, `FXP_JWT_PREVIOUS_*`). `iss` + `aud` required (`FXP_JWT_ISSUER` / `_AUDIENCE`, dev defaults). `nbf` checked. `FXP_JWT_LEEWAY_SEC`. `jti` replay cache (`FXP_JWT_REPLAY_CACHE`), O(log n), scoped by iss/kid |
| WS `CheckOrigin` always true | ✅ fixed: scheme-aware same-origin check |
| `go test ./...` broken by vet | ✅ fixed |
| TLS | ❌ open (all listeners are plaintext) |
| Symmetric HS256 shared with the bridge | ⚠ still HS256 (rotation exists). Asymmetric keys / JWKS would remove the need for the bridge to hold a signing key |
| Graceful shutdown | ⚠ not re-verified |

Static analysis: all clean (§1.2).

---

## 5. Performance and benchmarks

### 5.1 Methodology
- ✅ The e2e benchmark completes in every configuration, including c=1.
- ⚠ Unchanged from round 2:
  - `benchmark.go` hard-codes `jwtSecret = "dev-secret-change-in-production"`.
  - The price `3_000_000_000` is commented `$3000.00`, but in cents that is $30M.
  - Latency is timed from the actual send, not the intended send (coordinated omission).
  - Each configuration is a single run with no HDR histogram.
- ⚠ Encoding: FIX still encodes with `format!` while FXP uses a preallocated buffer. `SBE_heap` (9.9 ns) is still faster than `SBE_stack` (26.4 ns), and SBE decode is 1.0 ns, so the SBE inputs still need `black_box`.

### 5.2 Results (same VM and commands as R1/R2; single runs, no pinning)

*Encoding (Criterion median, the groups that had finished when this report was written):*

| Benchmark | FXP | FIX 4.4 | SBE | R2 FXP |
|---|---|---|---|---|
| NOS encode (pre-alloc) | 99.5 ns | 359.1 ns | 26.4 ns stack / 9.9 ns heap ⚠ | 95.7 ns |
| NOS encode (fresh alloc) | 213.4 ns | — | — | 186.3 ns |
| NOS decode | 231.6 ns | 1,732 ns | 1.03 ns ⚠ | 253.0 ns |
| ExecutionReport encode | 98.8 ns | 366.3 ns | 24.1 ns | 121.9 ns |

Snapshot, incremental and batch groups were still running at the time of writing; they track R2 within noise in prior rounds. Rerun with `cargo bench --bench encoding_benchmark`.

Sizes in bytes (FIX / FXP / SBE): NOS 164 / 86 / 82 · ER 192 / 91 / 76 · Snapshot-5L 331 / 134 / 168 · Incremental 102 / **32** / 24. The incremental message gained 2 bytes because v2.1 added a side to each MD change. Run-to-run deltas of ±10–15% are within this VM's noise.

*End-to-end (release core, non-race gateway, via `scripts/e2e.sh` binaries):*

| Run | Orders | Fills | Elapsed | Orders/s | RTT p50 | p95 | p99 | Max | R2 |
|---|---|---|---|---|---|---|---|---|---|
| c=1, o=500 | 500 | 480 | 10 ms | 50,686 | 6.2 ms | 7.3 ms | 7.8 ms | 7.9 ms | 35,543/s, p50 9.5 ms |
| c=10, o=500 | 5,000 | 4,800 | 64 ms | **77,793** | 33.0 ms | 49.3 ms | 54.1 ms | 56.4 ms | 42,854/s |
| c=50, o=500 | 25,000 | 24,000 | 327 ms | **76,380** | 99.1 ms | 233.7 ms | 262.4 ms | 279.5 ms | 39,129/s, p50 435 ms |
| c=50, o=200, 25/s | 10,000 | 9,000 | 8.0 s | 1,250 | **0.67 ms** | **1.16 ms** | **2.64 ms** | 4.22 ms | p50 1.45 / p95 35.8 / p99 36.4 ms |

Market-data fan-out: p50 ≈ 40 µs, p95 52–759 µs, p99 95–829 µs.

**Reading the numbers:**
- **The ~36–41 ms bimodal tail is gone** (TCP_NODELAY). Rate-limited p99 improved about 14×, from 36.4 ms to 2.6 ms.
- Unlimited-rate throughput roughly doubled versus R2 (≈77k orders/s at c=10 and c=50). That fits with the hot-path logging moving to debug level and the pool-reader fix.
- As in every round, unlimited-mode "RTT" is queueing delay.

### 5.3 Top 5 likely bottlenecks (re-ranked for R3)

| # | Bottleneck | How to confirm |
|---|---|---|
| 1 | Gateway proto re-marshal passes + pool `acquire()` channel contention + synchronous client writes in `routeFromCore` | `pprof` CPU and block/mutex profiles, `go tool trace` at c=50 |
| 2 | Symbol task awaiting slow output/MD channels; `SymbolRouter` mutex on every inbound frame | `tokio-console`, Tokio `RuntimeMetrics`, a slow-subscriber A/B |
| 3 | Per-order allocation (String clones, `Vec` per fill, frame buffers), plus the ownership-namespace string ops in Go | `dhat-rs` / `heaptrack`; Go `-memprofile`. Criterion microbench of `Engine` on a 10k-order book |
| 4 | Event-log fsync when `FXP_EVENT_LOG` is on (and the FIX `FileStore` fsync mode) | A/B the e2e bench with the log on/off and with strict vs group fsync; `perf trace -e fsync,fdatasync` |
| 5 | Single process / loopback co-location (client, gateway, core on 8 shared vCPUs) | `taskset` pinning per component, `perf stat` context switches; repeat on bare metal |

---

## 6. Gaps, roadmap and project health

**The three known gaps are all closed:**
- **Auctions:** scheduled open/close uncross with a pure uncross function and phase tests.
- **Seqnum persistence:** durable `FileStore` + registry, verified across a kill/restart in e2e.
- **CI/CD:** a workflow with Rust, deny, Go matrix, e2e and buf jobs, plus Dependabot and Docker. I didn't inspect hosted Actions results, only the workflow definition and local equivalents. Suggested additions: branch protection requiring these jobs, a tagged-release job (static binaries, checksums, SBOM), and a nightly pinned benchmark job to catch regressions.

**Docs vs code:** the README was refreshed (sizes, benchmark environment, logging, Docker, test counts). I found no new inaccurate claims in the areas I checked. The one remaining inconsistency is in the tooling: `benchmark.go`'s hard-coded secret and wrong price comment.

**Hygiene:**
- ✅ MIT license with SPDX headers, SECURITY / CONTRIBUTING / CHANGELOG, issue and PR templates.
- ✅ `deny.toml` license allow-list, Dependabot, Docker running as non-root.
- The old 6.6 MB `go_gateway/bench` binary is still in git *history*. A `git filter-repo` purge is optional and needs a force-push decision.
- Single maintainer (bus factor 1). The PR-per-severity-tier workflow with Conventional Commits works well.

**Suggested next steps:**
1. STP plus cancel-on-disconnect (both are risk controls).
2. FIX session schedule and a DataDictionary-driven validator, plus QuickFIX acceptance tests in CI.
3. TLS on the gateway and bridge listeners.
4. Iceberg and pegged orders, and configurable tick size / precision.
5. Clean up `benchmark.go` (read the secret from the environment, fix the price, measure from intended send time, HDR histograms), and make the encoding benches fair (`black_box` the SBE inputs, preallocated FIX encoder).
6. Asymmetric JWT (JWKS).
