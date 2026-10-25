# FXP Protocol

A high-performance binary trading protocol and matching engine built as an open-source alternative to FIX 4.x for institutional electronic trading infrastructure.

**Stack:** Rust matching engine · Go gateway · Protobuf binary encoding · JWT authentication

---

## Why FXP?

FXP combines a versioned binary schema, session handling and a matching engine.
The corrected local NewOrder fixture measurements are:

| Operation | FXP | FIX | SBE |
|---|---:|---:|---:|
| Encode, reused buffer / SBE stack | 149.3 ns | 133.8 ns | 29.6 ns |
| Decode, owned output | 377.2 ns | 1938.5 ns generic tag map | 110.2 ns |
| Measured encode+decode | 535.1 ns | 2150.7 ns | 122.1 ns |
| Fixture size, timestamp/TIF included | 94 B | 164 B | 95 B |

**Reusable FIX encoding is faster than FXP in this run.** Decoder implementations
and field layouts differ; this table is not a universal protocol speed ranking.
Shared Linux VM, loopback, single host, no explicit pinning. See
[full results, five-run spreads and caveats](Benchmark.md).

---

## Architecture

```
Client (TCP/WebSocket)
    │  FXP binary frames (4-byte length prefix + Protobuf body)
    ▼
Go Gateway  :8082
    ├── JWT authentication (HS256)
    ├── Session management
    ├── Connection pool (`runtime.NumCPU()` default; 5 on the recorded benchmark host)
    └── orderClientMap (routes ExecutionReports back to originators)
    │
    ▼
Rust Core  :8080
    ├── SymbolRouter: one Tokio task per symbol, messages delivered over mpsc
    ├── Symbol-to-task map guarded by a global mutex
    ├── BTreeMap price-time priority order book
    └── Full order type support: Market, Limit, IOC, FOK, Stop, StopLimit
    │
    ▼
Client ← ExecutionReport + MarketDataIncrementalUpdate
```

**Key design decisions:**
- Per-symbol Tokio tasks own their order books; the symbol-to-task map is protected by a global mutex
- Tokio `mpsc` channels carry messages to the socket writer
- The Go connection pool defaults to `runtime.NumCPU()` and can be tuned with `SetPoolSize`; the measured host exposed 5 logical CPUs
- Protobuf binary encoding with reusable buffers; measured implementations and allocation variants are documented in Benchmark.md

---

## Order Types

| Type | Behaviour | Status |
|---|---|---|
| `LIMIT` DAY/GTC/GTD | Continuous matching; during a call, rests without matching until uncross | ✅ |
| `LIMIT` IOC | Fill available qty, cancel remainder; rejected during calls | ✅ |
| `LIMIT` FOK | Fill all or cancel all; rejected during calls | ✅ |
| `MARKET` | Continuous: sweep then cancel remainder. Call: queue off-book until uncross, then cancel remainder | ✅ |
| `STOP` | Parks until a continuous print or opening auction price triggers it as a market order; never triggers during calls or on the closing print | ✅ |
| `STOP_LIMIT` | Same trigger rules as STOP, executes as a limit | ✅ |
| `AT_OPEN` | Limit/market accepted only during OpeningCall; unfilled remainder canceled at open | ✅ Full schedule required |
| `AT_CLOSE` | Limit/market held off-book during Continuous/OpeningCall, joins ClosingCall; remainder canceled at close | ✅ Full schedule required |

Day orders expire at configured close, after the closing uncross. GTD orders expire at midnight UTC following their date, with or without a schedule. GTC orders survive sessions but are not persisted across process restarts. Price levels use FIFO order (`VecDeque::push_back` on add and `pop_front` on match), covered by `p01_time_priority_fifo_within_level`.

Closed rejects new orders and replaces but permits cancellation. Calls may expose a crossed limit book; they never run continuous matching. Replaces during a call lose priority and do not match. Held AT_CLOSE orders can be canceled but not replaced. When moved at close_call, held limits retain their original arrival order relative to each other but queue behind orders already resting at the same price. Auction-only TIFs are not supported for stops. With no full schedule, AT_OPEN and AT_CLOSE are rejected.

Auction price selection maximizes volume, minimizes imbalance, prefers the reference (last trade) price, then resolves pressure ties. Allocations use market/price/time priority. Auction fills have `auction=true`; opening prints elect parked stops after the Continuous phase status, while closing prints update the reference without electing stops. TradingSessionStatus and deduplicated AuctionInfo updates are streamed to symbol subscribers.

**Cancel workflows:**
- `OrderCancelRequest` — cancel a working order
- `OrderCancelReplaceRequest` — cancel the original and add a replacement; the replacement loses the original's FIFO priority
- `OrderMassCancelRequest` — cancel all of the caller's orders (including parked stops), optionally by symbol and side

**Risk controls** (all off by default, see [docs/risk-controls.md](docs/risk-controls.md)): self-trade prevention per order or via `FXP_STP_DEFAULT` (cancel-newest, cancel-oldest, cancel-both, decrement-and-cancel; scoped to the owner or an STP group), owner-scoped mass cancel, and per-session cancel-on-disconnect.

---

## Proto Schema (v2.4)

Full institutional-grade message coverage:

```
NewOrderSingle          — order entry with stop_price, expire_date, account
OrderCancelRequest      — cancel working order
OrderCancelReplaceRequest — amend price/quantity
ExecutionReport         — lifecycle reports with leaves_qty, avg_px, cum_qty
MarketDataSnapshot      — full order book depth
MarketDataIncrementalUpdate — level changes
TradeCaptureReport      — trade print feed
TradingSessionStatus    — trading phase by symbol
AuctionInfo             — indicative auction price, volume and imbalance
ErrorMessage            — structured error responses
OrderMassCancelRequest / OrderMassCancelReport — owner-scoped mass cancel
```

Version 2.4 adds optional iceberg display quantities on entry/replacement and execution reports; see [iceberg behavior](docs/iceberg-orders.md). Version 2.3 adds owner-scoped order-status request/response messages and `EXEC_ORDER_STATUS` (10). Version 2.2 adds self-trade prevention (`NewOrderSingle.stp_mode`/`stp_group`, `EXEC_RESTATED`), `Logon.cancel_on_disconnect` and the mass-cancel messages, all additively. Version 2.1 adds auction/session messages and an ExecutionReport auction flag without changing existing wire identifiers. Version 2.0 shifted all existing enum numbers by one and is wire-breaking against 1.x. All enums use a per-enum `*_UNSPECIFIED = 0` sentinel; existing named values start at 1. Unknown and unspecified order-entry enums are rejected. In an `OrderCancelReplaceRequest`, `new_time_in_force = TIME_IN_FORCE_UNSPECIFIED (0)` means keep the current value.

**TimeInForce:** `TIME_IN_FORCE_UNSPECIFIED=0` · `DAY=1` · `GTC=2` · `IOC=3` · `FOK=4` · `AT_OPEN=5` · `AT_CLOSE=6` · `GTD=7`

**ExecutionType:** EXEC_NEW · EXEC_FILLED · EXEC_CANCELED · EXEC_REPLACED · EXEC_REJECTED · EXEC_EXPIRED · EXEC_RESTATED

Custom firm extensions: field numbers 1000–1999 reserved per message.

---

## Quick Start

### Prerequisites

- Rust toolchain (`rustup`)
- Go 1.25.0+ (the modules select Go 1.26.10)
- `protoc` (the generation script installs the pinned `protoc-gen-go` plugin when needed)
- Python 3 for the FIX end-to-end suite

`scripts/gen_proto.sh` and `scripts/e2e.sh` need `go` on `PATH`; if it is not, they fall back to `/usr/local/go/bin/go` and fail with an error when neither exists.

### 1. Generate protobuf bindings

```bash
./scripts/gen_proto.sh
```

Equivalent raw command:

```bash
cd fxp_core/proto && mkdir -p ../../go_gateway/protobuf && protoc --go_out=../../go_gateway/protobuf --go_opt=paths=source_relative fxp.proto
```

### 2. Start Rust Core

```bash
cd fxp_core
cargo run
# INFO fxp_core::server: FXP Core running on 127.0.0.1:8080
```

### 3. Start Go Gateway

```bash
cd go_gateway
export FXP_JWT_SECRET="dev-secret-change-in-production"
go run .
# level=INFO msg="✅ TCP Server started on 127.0.0.1:8082"
```

### 4. Run the encoding benchmarks

```bash
cd fxp_core
cargo bench --bench encoding_benchmark
# Prints size comparison table + criterion timing results
```

### 5. Run the end-to-end benchmark

With a fresh release core and gateway running and compatible `FXP_JWT_*` settings:

```bash
cd go_gateway
go run ./benchmark --mode open --rate 1000 --concurrency 1 --orders 1000 --warmup 100 --repetitions 5
go run ./benchmark --mode closed --concurrency 1 --orders 1000 --warmup 100 --repetitions 5
```

JSON contains per-run HDR histograms, separate ACK/final-fill endpoints and
median/min/max summaries; Markdown is printed and saved alongside it. Open-loop
latency includes sender backlog from intended send times. See the
[methodology and limitations](docs/benchmark-methodology.md).

### Logging

Logs go to stderr. Per-order, per-fill and per-message events are logged at debug level and are off by default.

| Component | Variable | Default | Example |
|---|---|---|---|
| `fxp_core` | `RUST_LOG` ([`EnvFilter`](https://docs.rs/tracing-subscriber/latest/tracing_subscriber/filter/struct.EnvFilter.html) syntax) | `info` | `RUST_LOG=fxp_core=debug` |
| `fxp_core` | `FXP_SESSION_SCHEDULE` | unset (no Day expiry) | `close=16:30:00` |
| `fxp_core` | `FXP_STP_DEFAULT` (`none`, `cancel-newest`, `cancel-oldest`, `cancel-both`, `decrement-and-cancel`): self-trade prevention for orders that do not set `stp_mode` | `none` | `FXP_STP_DEFAULT=cancel-newest` |
| `fxp_core` | `FXP_SYMBOLS` (comma-separated symbol allow-list; unset allows any symbol) | unset | `FXP_SYMBOLS=BTC-USD,ETH-USD` |
| `fxp_core` | `FXP_MAX_SYMBOLS` (positive maximum number of admitted symbols) | unlimited | `FXP_MAX_SYMBOLS=500` |
| `fxp_core`, `go_gateway`, `fix_bridge` | `FXP_SHUTDOWN_TIMEOUT_SEC` (graceful-shutdown deadline) | `5` | `FXP_SHUTDOWN_TIMEOUT_SEC=10` |
| `go_gateway`, `fix_bridge` | `FXP_LOG_LEVEL` (`debug`, `info`, `warn`, `error`) | `info` | `FXP_LOG_LEVEL=debug` |

Existing symbols restored from the core event log remain available even if
they are outside the current allow-list or symbol limit; new symbols still
follow the configured admission policy.

Core, gateway, and bridge handle SIGINT/SIGTERM and shut down within
`FXP_SHUTDOWN_TIMEOUT_SEC` (default 5 seconds). Compose grants each service a
15-second stop grace period and uses an init process to forward signals. See
[docs/operations.md](docs/operations.md) for backpressure and shutdown behavior.

The optional core session schedule is a strict comma-separated `key=value`
list of UTC `HH:MM[:SS]` times. Only two forms are valid:
- `close=16:30`: always Continuous, with daily Day expiry (no auctions).
- `open_call=08:30,open=09:00,close_call=16:25,close=16:30`: all four keys,
  strictly ordered within one day. OpeningCall is [open_call, open),
  Continuous is [open, close_call), ClosingCall is [close_call, close),
  and the market is Closed otherwise.

Unknown/duplicate keys, partial schedules, unordered times, invalid times and
an explicitly empty value prevent startup. Unset means always Continuous and
no Day expiry. With a schedule, resting Day orders and parked Day stops expire
at close. Phase and expiry boundaries advance before each request and on a
one-second timer, including every missed daily auction after a time jump.

GTD orders require `expire_date=YYYY-MM-DD` (FIX `59=6`, `432=YYYYMMDD`).
They expire at midnight UTC following that date, independently of the session
schedule. Missing, malformed and already-expired dates are rejected. Dates on
non-GTD orders are ignored. Cancel/replace retains the original date; changing
to GTD requires that retained date to be valid and not already expired.

### Docker

Optional single-node core persistence is enabled with `FXP_EVENT_LOG=<path>`.
Unset means fully disabled, with the existing in-memory behavior. Every engine
input is fsynced before application, and startup replays the input log before
accepting clients. All live orders are restored, not just GTC; missed expiry and
auction boundaries run on the first startup tick without subscribers.

The parent directory must already exist. In Compose, use
`FXP_EVENT_LOG=/data/core_store/events.log` for the named core volume.
The log is exclusively locked, schedule/version mismatches fail startup, and
torn/corrupt suffixes are truncated with a warning. **Gateway orderClientMap is
not restored**, so reports for restored orders cannot be routed to old clients.
There is no compaction, replication or exactly-once client delivery. Disabling
the flag does not migrate state; re-enabling an old log restores its history.
See [the persistence design and rollout guide](docs/gtc-persistence.md).

```bash
export FXP_JWT_SECRET="$(openssl rand -hex 32)"
export FXP_FIX_CREDENTIALS="FIRM01:$(openssl rand -hex 16)"
docker compose up --build
# WebSocket :8081, FXP TCP :8082, FIX 4.4 :8083 (core :8080 stays private)
```

The multi-stage [`Dockerfile`](Dockerfile) builds all three binaries into one image; [`docker-compose.yml`](docker-compose.yml) runs core, gateway and bridge from it and keeps the FIX session store in the `fix_store` volume. Compose refuses to start without `FXP_JWT_SECRET` and `FXP_FIX_CREDENTIALS`; other variables from the tables below can be added to the `environment:` blocks.

### 6. Run the trading day simulation

```bash
cd go_gateway

# Default: 5 symbols, 20 participants, 120 seconds
go run simulation/trading_day.go

# Larger simulation
go run simulation/trading_day.go --symbols 10 --participants 50 --duration 300

# Reproducible run
go run simulation/trading_day.go --seed 42 --verbose
```

---

## Trading Day Simulation

Simulates a realistic institutional trading session with 4 participant archetypes across 5 session phases.

**Participant types:**

| Type | Behaviour | Rate |
|---|---|---|
| Market Maker | Posts bid/ask quotes, re-quotes aggressively, occasional IOC crosses | 8 orders/sec |
| Institutional | Large limit orders, AT_OPEN/AT_CLOSE participation, 30% cancel/replace | 1 order/sec |
| Retail | Mix of market (50%), limit (30%), IOC (20%), small sizes | 0.5 orders/sec |
| Hedge Fund | Stop orders for risk management, aggressive IOC, FOK block trades | 2 orders/sec |

**Session phases** (scaled to wall-clock `--duration`):

| Phase | Duration | Behaviour |
|---|---|---|
| PRE_MARKET | 5% | Minimal activity |
| OPEN_AUCTION | 5% | 3× burst, AT_OPEN orders |
| CONTINUOUS | 80% | Normal matching |
| CLOSE_AUCTION | 8% | 2× burst, AT_CLOSE/MOC orders |
| CLOSED | 2% | Wind-down |

The simulation labels these as auction phases, but the matching engine currently treats AT_OPEN/AT_CLOSE orders as ordinary resting limit orders; it does not run auction batch matching.

**Sample output (120s, 5 symbols, 20 participants):**

```
Orders placed:     11,129       Fill rate:    56.4%
Orders filled:      6,275       Cancel rate:  32.8%
Orders cancelled:   3,647
Cancel/Replaces:      175       p50 RTT:      4.0ms
Stops fired:            0       p95 RTT:    154ms

AAPL   open=$185.00  last=$185.38  vol=76,682
MSFT   open=$375.00  last=$375.49  vol=52,553
GOOGL  open=$172.00  last=$171.12  vol=72,200
AMZN   open=$198.00  last=$197.07  vol=51,359
NVDA   open=$875.00  last=$875.15  vol=38,359
```

---

## FIX Message Support

The FIX 4.4 bridge (`fix_bridge`, port 8083) handles these inbound messages:

| MsgType | Message | Support |
|---|---|---|
| `A`, `0`, `1`, `2`, `3`, `4`, `5` | Logon, Heartbeat, TestRequest, ResendRequest, Reject, SequenceReset, Logout | ✅ Session layer; Logon `8013=Y` enables cancel-on-disconnect |
| `D` | NewOrderSingle | ✅ incl. STP `2362` SelfMatchPreventionID, `2964` SelfMatchPreventionInstruction (1/2/3, 99 = decrement-and-cancel) |
| `F` | OrderCancelRequest | ✅ |
| `G` | OrderCancelReplaceRequest | ✅ |
| `V` | MarketDataRequest (snapshot, subscribe, unsubscribe) | ✅ |
| `q` | OrderMassCancelRequest (`530=7` all orders, `530=1` by symbol; optional `54`) | ✅ answered with `35=r` (531/532/533); other 530 values rejected with `531=0 532=0` |
| `H`, `AF` | OrderStatusRequest / OrderMassStatusRequest | ✅ owner-scoped snapshots, including retained terminal orders; mass types 1 (symbol) and 7 (all) |
| any other application type | e.g. `AD`, `c` | ❌ BusinessMessageReject (`35=j`, `380=3`) |

Outbound: ExecutionReport (`8`, ExecType `D` = restated by decrement-and-cancel STP), OrderCancelReject (`9`), OrderMassCancelReport (`r`), MarketDataSnapshotFullRefresh (`W`), MarketDataIncrementalRefresh (`X`), BusinessMessageReject (`j`) plus the session messages.

Required fields follow the FIX 4.4 spec (`fix_bridge/codec/dictionary.go`):
`D` needs 11, 55, 54, 60, 40; `F` needs 41, 11, 55, 54, 60; `G` needs 41, 11, 55, 54, 60, 40;
`V` needs 262, 263, 264, 267, 269, 146, 55; `q` needs 11, 530, 60. A message missing one of them gets
Reject (`35=3`) with `373=1` (Required tag missing), `371` (the missing tag) and `372`.

## FIX Session Store

The FIX bridge keeps each session's sequence numbers and sent messages per
`(BeginString, SenderCompID, TargetCompID)`, so a client that reconnects — or
logs on again after a bridge restart — resumes where it left off and can
ResendRequest earlier messages. Each session has a directory holding a
CRC-protected append-only `outbound.log` (a torn tail is truncated on start)
and a `seqnums` file replaced atomically on every change.

| Variable | Default | Meaning |
|---|---|---|
| `FXP_FIX_STORE_DIR` | `fix_store` | Store root; set empty for in-memory state (lost on restart) |
| `FXP_FIX_STORE_FSYNC` | `strict` | `strict`: fsync every write; `group`: fsync every `FXP_FIX_STORE_GROUP_INTERVAL` (default `10ms`); `async`: never fsync. All modes survive a killed process; they differ only on power loss |
| `FXP_FIX_STORE_MAX_MESSAGES` | `100000` | Outbound messages retained for resend; older ones are answered with SequenceReset-GapFill |

A second Logon for a session that is already logged on is rejected. Session
schedules and reset policies are optional; see [FIX conformance](docs/fix-conformance.md).

### Gateway authentication

Clients log on with an HS256 JWT. The header `alg` must be `HS256`
(`none` and every other algorithm are rejected before the MAC is checked);
the claims must carry `sub`, `exp`, `iat`, the configured `iss` and `aud`;
`nbf` is honoured with a small leeway. The bridge, `devjwt`, the benchmark and
the simulation read the same variables when minting.

| Variable | Default | Meaning |
|---|---|---|
| `FXP_JWT_SECRET` | (required) | Current HS256 key |
| `FXP_JWT_KID` | unset | `kid` of the current key; tokens without `kid` always use the current key |
| `FXP_JWT_PREVIOUS_KID` / `FXP_JWT_PREVIOUS_SECRET` | unset | Previous key, still accepted for tokens carrying this `kid` during a rotation |
| `FXP_JWT_ISSUER` | `fxp-dev` | Required `iss` |
| `FXP_JWT_AUDIENCE` | `fxp-gateway` | Required `aud` (string or array) |
| `FXP_JWT_LEEWAY_SEC` | `30` | Clock-skew leeway on `nbf` |
| `FXP_JWT_REPLAY_CACHE` | off | `on`: require `jti` and reject a reused `jti` until its token expires (per gateway process) |
| `FXP_GW_WS_ALLOWED_ORIGINS` | unset | Comma-separated browser origins allowed to open the WebSocket. Same-origin requests and clients sending no `Origin` header are always allowed; any other origin gets 403 |
| `FXP_GW_CLIENT_QUEUE` | `8192` | Bounded per-client queue for execution reports and other critical output; overflow closes the client |
| `FXP_GW_MD_QUEUE` | `4096` | Bounded per-client market-data queue; overflow drops market data and increments `MarketDataDropped()` |

## Tests

Counts observed at this revision:

| Suite | Test count |
|---|---:|
| `fxp_core` (`cargo test`) | 163 unit tests |
| `go_gateway` (`go test ./...`) | 49 top-level tests |
| `fix_bridge` (`go test ./...`) | 123 top-level tests |
| Python FIX E2E client (`scripts/e2e.sh`) | 69 main checks, plus cancel-on-disconnect and restart/persistence checks |

---

## Benchmark Results

Five repetitions on a shared Linux VM (AMD EPYC 7763, 5 visible CPUs), Rust
1.99.1 and Go 1.26.10; Go/core each configured with two workers, affinity 0–4
without explicit pinning. Live counts are exact, with a separate 100-order
warmup and 1000 measured orders per connection per run.

| Scenario | Completed orders/s | ACK p50 / p99 ms | Final-fill p50 / p99 ms |
|---|---:|---:|---:|
| open-c1 | 999.7 | 0.777 / 1.537 | 1.337 / 2.466 |
| open-c10 | 9969.5 | 0.961 / 2.542 | 1.688 / 3.334 |
| closed-c1 | 3901.1 | 0.226 / 0.453 | 0.353 / 0.809 |


Values are medians of per-run statistics; [Benchmark.md](Benchmark.md) includes
all min/max spreads, p90/p99.9/max, encoding confidence intervals, raw artifacts
and commands. Open-loop offers 1000 orders/s/connection and measures from intended
send time, including backlog. Closed-loop is ACK-controlled. Finite paired-order
bursts and loopback figures are not production capacity or WAN SLO claims.

---

## Repository Structure

```
fxp/
├── fxp_core/                    Rust matching engine
│   ├── src/
│   │   ├── server.rs            TCP server, per-symbol dispatch, stop trigger logic
│   │   ├── lib.rs               Unit tests
│   │   └── order_book/
│   │       ├── order.rs         Order struct, OrderType, TimeInForce enums
│   │       ├── order_book.rs    BTreeMap price-time priority book
│   │       ├── matcher.rs       Aggressive matching: Market/IOC/FOK/Stop
│   │       └── execution_report.rs
│   ├── benches/
│   │   └── encoding_benchmark.rs  Criterion benchmarks vs FIX and SBE
│   └── proto/
│       └── fxp.proto            Protocol schema v2.4
│
├── go_gateway/                  Go gateway
│   ├── logging/                 slog setup (FXP_LOG_LEVEL), shared with fix_bridge
│   ├── transport/
│   │   ├── pool.go              Pool defaults to runtime.NumCPU()
│   │   ├── tcp_server.go        TCP server, auth routing
│   │   ├── common.go            Message router, market data fan-out
│   │   ├── ws_server.go         WebSocket server
│   │   └── auth.go              JWT validation, session management
│   ├── benchmark/
│   │   └── benchmark.go         End-to-end latency and throughput benchmark
│   └── simulation/
│       └── trading_day.go       Trading day simulation
│
├── fix_bridge/                  FIX 4.4 session, codec, and translators
│   ├── main.go
│   ├── codec/
│   ├── session/
│   └── translator/
├── fix_test_client/
│   └── fix_client.py            FIX E2E scenarios
├── scripts/
│   ├── gen_proto.sh             Generate protobuf bindings
│   └── e2e.sh                   Start core → gateway → bridge and run the FIX E2E suite
├── Dockerfile                   Multi-stage build of all three binaries
├── docker-compose.yml           core + gateway + bridge, secrets from the environment
└── Benchmark.md                 Full benchmark methodology and results
```

---

## Roadmap

- [ ] Opening/closing auction batch matching at phase transitions
- [ ] Stop/StopLimit trigger on live last-trade-price feed
- [ ] `OrderCancelRequest` / `OrderCancelReplaceRequest` full round-trip in simulation
- [x] Release build benchmarks
- [x] CI/CD pipeline (GitHub Actions: `.github/workflows/ci.yml`)
- [ ] TLS support (infrastructure exists in proto; not yet wired)
- [x] FIX 4.x bridge (inbound FIX → FXP translation layer)

---

## License

MIT
### FIX session schedule configuration

All settings are optional. See [FIX conformance](docs/fix-conformance.md) for boundaries and archival behavior.

| Variable | Default | Meaning |
|---|---|---|
| `FXP_FIX_DICTIONARY_STRICT` | `false` | Reject undeclared user-defined tags (>=5000); standard tags always validated |
| `FXP_FIX_START_TIME` / `FXP_FIX_END_TIME` | unset | Local `HH:MM:SS` window; both required to enable |
| `FXP_FIX_START_DAY` / `FXP_FIX_END_DAY` | unset | Full weekday names; both enable a weekly window |
| `FXP_FIX_TIME_ZONE` | `UTC` | IANA timezone |
| `FXP_FIX_RESET_ON_LOGON` | `false` | Reset on authenticated Logon; requires inbound sequence 1 |
| `FXP_FIX_RESET_ON_LOGOUT` | `false` | Reset after Logout |
| `FXP_FIX_RESET_ON_DISCONNECT` | `false` | Reset after a logged-on connection ends |

### FIX conformance

The bridge loads the pinned QuickFIX FIX 4.4 XML dictionary at startup, with
[documented venue extensions](fix_bridge/codec/spec/README.md). It validates
required tags, membership, datatypes, enums, duplicates and repeating-group
counts/order; invalid messages receive Reject(3) with 371/372/373. See
[conformance details](docs/fix-conformance.md). Unsupported application message types
remain BusinessMessageReject(j). Dictionary validation changes an invalid
Side enum from an ExecutionReport rejection to session Reject reason 5.

Order status replies use ExecutionReport `150=I`, `17=0`, the current `39`,
and original cumulative/leaves quantities. Queries do not redirect fills.
Single status echoes OrdStatusReqID(790); mass status echoes 584 and includes
911/912 count/final markers. Unknown/foreign IDs and empty mass results get
BusinessMessageReject (`380=1`). See [status retention and consistency](docs/fix-conformance.md#order-status).

The CI `quickfix-acceptance` job runs 59 pinned QuickFIX v1.15.1 FIX44 server
definitions with its official POSIX runner. Observed local result: **28 passed,
31 failed and explicitly allow-listed, 0 unexpected failures / runner errors**.
The [per-case reasons](tests/quickfix/expected-failures.json) preserve unsupported
features and policy differences; this is a regression gate, not certification.
See [runner/adapter details](tests/quickfix/README.md) and the command in
[CONTRIBUTING.md](CONTRIBUTING.md#quickfix-fix44-acceptance).

Status batches are bounded by both 64 reports and the gateway's 1 MiB frame
limit, so large valid order identifiers cannot disconnect the core pool.
Dictionary `MONTHYEAR` accepts only real dates or weeks 1–5; invalid Boolean
values and negative nonnegative-integer fields use Reject reason 5, while
malformed integer syntax uses reason 6.

### Symbol tick, precision and quantity configuration

Set `FXP_SYMBOL_CONFIG` on the core and FIX bridge to the same CSV file.
Unset means the existing defaults: 2 decimal places, one-minor-unit tick,
lot/minimum 1, maximum 2,147,483,647. See [symbol configuration](docs/symbol-config.md)
and [example CSV](config/symbols.example.csv). This does not change prices on
the wire: they remain signed int64 minor units. Native clients must use the
same symbol scales. FIX execution reports and market data use those scales.

| Variable | Default | Components | Meaning |
|---|---|---|---|
| `FXP_SYMBOL_CONFIG` | unset | core, FIX bridge | Validated per-symbol CSV, loaded once at startup; in Compose use `/etc/fxp/symbols.example.csv` or another file under the mounted `config/` directory |

Price-only amendments retain STP-restated totals even below entry quantity
minimums; explicit new totals still undergo lot/min/max validation.

Unused market-order limit prices and unused non-stop trigger fields are ignored
by core tick validation. Active limit/trigger prices still require tick alignment.

## Reserve and pegged orders

Limit DAY/GTC/GTD orders can opt into reserve using FIX MaxFloor (111) or native
`display_quantity`; zero keeps fully displayed behavior. Only the current slice
appears in market data. Replenishment moves the order to the back of its price
level, and private reports accumulate fills against the total. Replace resets the
slice and priority; omitted MaxFloor retains it and explicit zero disables it.
Iceberg IOC/FOK, stops and full-auction schedules are rejected. Ordinary IOC/FOK
orders can consume reserve with the existing STP policy. See the complete
[iceberg contract](docs/iceberg-orders.md) and [symbol precision](docs/symbol-config.md).


Pegged orders (Stage C) are deferred; the [design note](docs/pegged-orders.md)
specifies references, cycles, priority, caps, interactions and acceptance tests.
They are not enabled by this change.

## Nightly benchmarks and draft releases

`bash scripts/bench.sh target/bench` builds an isolated release stack, runs
five-repetition intended-time/open and labeled closed-loop scenarios, then the
corrected encoding suite. JSON, Markdown, raw Criterion estimates and actual
build/affinity metadata are retained. Nightly/manual [bench.yml](.github/workflows/bench.yml)
uses `ubuntu-24.04` with no numerical regression threshold.

Tagged [release.yml](.github/workflows/release.yml) builds static Linux amd64 and
arm64 core/gateway/bridge archives, SHA256SUMS and CycloneDX inventories, and
attaches them to a draft for manual review. Manual workflow runs build artifacts
only. See [required checks and repository configuration](docs/releasing.md).

### TLS and core trust

Plaintext remains the development default. Gateway TCP/WebSocket and FIX listeners
warn when bound outside loopback without TLS. Enable TLS and require it in deployed
services; JWT/FIX authentication remains required. The core always binds
`127.0.0.1:8080`; set the same distinct token on core and gateway to admit only
trusted forwarding connections. The bridge reaches the core through the gateway.
See [deployment, reload and client examples](docs/tls-trust.md) and the
[trust model](SECURITY.md#trust-model).

| Variable | Default | Applies to / behavior |
|---|---|---|
| `FXP_TLS_CERT_FILE` / `FXP_TLS_KEY_FILE` | unset | Gateway and bridge server certificate/key PEM; both required to enable TLS 1.2–1.3 |
| `FXP_TLS_CLIENT_CA_FILE` | unset | Gateway and bridge: require and verify client certificates against this PEM CA bundle |
| `FXP_TLS_REQUIRED` | `false` | Gateway and bridge refuse startup without TLS; malformed booleans fail startup |
| `FXP_GW_TCP_ADDR` | `:8082` | Gateway native TCP bind address |
| `FXP_GW_WS_ADDR` | `:8081` | Gateway WebSocket bind address (wss when TLS enabled) |
| `FXP_CORE_AUTH_TOKEN` | unset | Core and gateway only: optional shared connection token, maximum 1024 UTF-8 bytes; use a random secret distinct from JWT |
| `FXP_GATEWAY_TLS` | `false` | Bridge enables verified TLS to the gateway |
| `FXP_GATEWAY_TLS_CA_FILE` | system roots | Bridge's trusted gateway CA bundle |
| `FXP_GATEWAY_TLS_CERT_FILE` / `FXP_GATEWAY_TLS_KEY_FILE` | unset | Bridge's optional upstream mTLS identity |
| `FXP_GATEWAY_TLS_SERVER_NAME` | dial host | Bridge's verified upstream server name |
| `FXP_CLIENT_TLS` | `false` | Go TCP/WS clients, benchmark and Python FIX client enable verified TLS |
| `FXP_CLIENT_TLS_CA_FILE` | system roots | Tool clients' trusted server CA bundle |
| `FXP_CLIENT_TLS_CERT_FILE` / `FXP_CLIENT_TLS_KEY_FILE` | unset | Tool clients' optional mTLS identity |
| `FXP_CLIENT_TLS_SERVER_NAME` | dial host | Tool clients' verified server name |

Server and bridge settings are exposed in Compose; tool-only `FXP_CLIENT_*` settings
are for host clients. Compose mounts `./certs:/etc/fxp/certs:ro`; configure container
paths and ensure UID 10001 can read the key. Certificate/key/client-CA file contents
are checked every second and new connections use the last valid snapshot.
`bash scripts/e2e_tls.sh` exercises all listeners with mTLS, live certificate
rotation, authenticated core forwarding, the full FIX/order/restart checks and the
benchmark smoke scenarios. Test certificates are temporary and deleted on exit.
