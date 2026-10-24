# FXP Protocol

A high-performance binary trading protocol and matching engine built as an open-source alternative to FIX 4.x for institutional electronic trading infrastructure.

**Stack:** Rust matching engine · Go gateway · Protobuf binary encoding · JWT authentication

---

## Why FXP?

FIX 4.x is the industry standard for electronic order routing, but it has fundamental limitations:

- **Text encoding:** ASCII tag=value format is verbose and slow to parse
- **No schema evolution:** Adding fields requires new protocol versions and breaks existing parsers
- **Single-threaded bottleneck:** Traditional FIX engines process messages sequentially

FXP addresses all three:

| Metric | FXP (Protobuf) | FIX 4.4 text | Improvement |
|---|---|---|---|
| `NewOrderSingle` size | 86 bytes | 164 bytes | **48% smaller** |
| `ExecutionReport` size | 91 bytes | 192 bytes | **53% smaller** |
| `MarketDataSnapshot` (5L) | 134 bytes | 331 bytes | **60% smaller, beats SBE** |
| Encode throughput | 10.9M msg/sec | 1.6M msg/sec | **6.7x faster** |
| Decode throughput | 2.3M msg/sec | 335K msg/sec | **6.9x faster** |
| Round-trip (encode+decode) | 527 ns | 3,606 ns | **6.8x faster** |

End-to-end (order → ExecutionReport): **p50 ≈ 0.45 ms, p99 ≈ 1.2–2.5 ms** at 50 concurrent senders × 25 orders/s in the release-build benchmark (Rust `--release`, Go `go build`, Linux; see [Benchmark Results](#benchmark-results)).

---

## Architecture

```
Client (TCP/WebSocket)
    │  FXP binary frames (4-byte length prefix + Protobuf body)
    ▼
Go Gateway  :8082
    ├── JWT authentication (HS256)
    ├── Session management
    ├── Connection pool (`runtime.NumCPU()` default; 8 on this benchmark machine)
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
- The Go connection pool defaults to `runtime.NumCPU()` and can be tuned with `SetPoolSize`; the benchmark machine had 8 logical CPUs
- Protobuf binary encoding with pre-allocated buffers — 10.9M encodes/sec single thread

---

## Order Types

| Type | Behaviour | Status |
|---|---|---|
| `LIMIT` DAY/GTC/GTD | Rests on book, passive matching | ✅ |
| `LIMIT` IOC | Fill available qty, cancel remainder | ✅ |
| `LIMIT` FOK | Fill all or cancel all (liquidity check first) | ✅ |
| `MARKET` | Sweep book at any price, cancel unfilled | ✅ |
| `STOP` | Parks until `last_trade_price` crosses `stop_price`, then fires as market | ✅ |
| `STOP_LIMIT` | Parks until stop triggers, then rests as limit | ✅ |
| `AT_OPEN` | LIMIT orders rest on the book; no opening-auction matching | ❌ Not supported |
| `AT_CLOSE` | LIMIT orders rest on the book; no closing-auction matching | ❌ Not supported |

Resting orders are held in an in-memory book. `expire_date` is stored but not enforced, so DAY/GTD orders do not expire automatically and GTC orders are not persisted across process restarts. Price levels use FIFO order (`VecDeque::push_back` on add and `pop_front` on match), covered by `p01_time_priority_fifo_within_level`.

**Cancel workflows:**
- `OrderCancelRequest` — cancel a working order
- `OrderCancelReplaceRequest` — cancel the original and add a replacement; the replacement loses the original's FIFO priority

---

## Proto Schema (v2.1)

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
```

Version 2.1 adds auction/session messages and an ExecutionReport auction flag without changing existing wire identifiers. Version 2.0 shifted all existing enum numbers by one and is wire-breaking against 1.x. All enums use a per-enum `*_UNSPECIFIED = 0` sentinel; existing named values start at 1. Unknown and unspecified order-entry enums are rejected. In an `OrderCancelReplaceRequest`, `new_time_in_force = TIME_IN_FORCE_UNSPECIFIED (0)` means keep the current value.

**TimeInForce:** `TIME_IN_FORCE_UNSPECIFIED=0` · `DAY=1` · `GTC=2` · `IOC=3` · `FOK=4` · `AT_OPEN=5` · `AT_CLOSE=6` · `GTD=7`

**ExecutionType:** EXEC_NEW · EXEC_FILLED · EXEC_CANCELED · EXEC_REPLACED · EXEC_REJECTED · EXEC_EXPIRED

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

```bash
cd go_gateway

# Throughput test
go run benchmark/benchmark.go --concurrency 10 --orders 500

# Latency test (rate-limited for honest RTT measurement)
go run benchmark/benchmark.go --concurrency 50 --orders 200 --rate 25 --timeout 60
```

### Logging

Logs go to stderr. Per-order, per-fill and per-message events are logged at debug level and are off by default.

| Component | Variable | Default | Example |
|---|---|---|---|
| `fxp_core` | `RUST_LOG` ([`EnvFilter`](https://docs.rs/tracing-subscriber/latest/tracing_subscriber/filter/struct.EnvFilter.html) syntax) | `info` | `RUST_LOG=fxp_core=debug` |
| `fxp_core` | `FXP_SESSION_SCHEDULE` | unset (no Day expiry) | `close=16:30:00` |
| `go_gateway`, `fix_bridge` | `FXP_LOG_LEVEL` (`debug`, `info`, `warn`, `error`) | `info` | `FXP_LOG_LEVEL=debug` |

The optional core session schedule is a strict comma-separated `key=value`
list. Currently only `close=HH:MM[:SS]` is supported, in UTC every day;
unknown or duplicate keys, invalid times and an explicitly empty value prevent
startup. Unset means Day orders never expire. With a schedule, resting Day
orders and parked Day stops expire at each close. The core advances expiry
before each request and on a one-second timer, including missed boundaries
after a time jump.

GTD orders require `expire_date=YYYY-MM-DD` (FIX `59=6`, `432=YYYYMMDD`).
They expire at midnight UTC following that date, independently of the session
schedule. Missing, malformed and already-expired dates are rejected. Dates on
non-GTD orders are ignored. Cancel/replace retains the original date; changing
to GTD requires that retained date to be valid and not already expired.

### Docker

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
| `A`, `0`, `1`, `2`, `3`, `4`, `5` | Logon, Heartbeat, TestRequest, ResendRequest, Reject, SequenceReset, Logout | ✅ Session layer |
| `D` | NewOrderSingle | ✅ |
| `F` | OrderCancelRequest | ✅ |
| `G` | OrderCancelReplaceRequest | ✅ |
| `V` | MarketDataRequest (snapshot, subscribe, unsubscribe) | ✅ |
| `H` | OrderStatusRequest | ❌ BusinessMessageReject (`35=j`, `380=3`) |
| any other application type | e.g. `AF`, `q`, `AD` | ❌ BusinessMessageReject (`35=j`, `380=3`) |

Outbound: ExecutionReport (`8`), OrderCancelReject (`9`), MarketDataSnapshotFullRefresh (`W`), MarketDataIncrementalRefresh (`X`), BusinessMessageReject (`j`) plus the session messages.

Required fields follow the FIX 4.4 spec (`fix_bridge/codec/dictionary.go`):
`D` needs 11, 55, 54, 60, 40; `F` needs 41, 11, 55, 54, 60; `G` needs 41, 11, 55, 54, 60, 40;
`V` needs 262, 263, 264, 267, 269, 146, 55. A message missing one of them gets
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
schedules / daily reset are not implemented; use `ResetSeqNumFlag=Y` (141=Y).

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

## Tests

Counts observed at this revision:

| Suite | Test count |
|---|---:|
| `fxp_core` (`cargo test`) | 64 unit tests |
| `go_gateway` (`go test ./...`) | 29 top-level tests |
| `fix_bridge` (`go test ./...`) | 82 top-level tests |
| Python FIX E2E client (`scripts/e2e.sh`) | 14 scenarios, 34 reported checks, plus 4 durable-session checks across a bridge restart |

---

## Benchmark Results

Full methodology and results: [`Benchmark.md`](Benchmark.md)

**Benchmark environment:** encoding timings and the debug-build throughput figures were measured on Windows 11, 8 logical cores, Rust 1.85.0, Go 1.24.1; message sizes are deterministic. The release-build latency figures were measured on a shared Linux KVM guest (no CPU pinning) with Rust 1.97 and Go 1.26.10. The code now requires Rust edition 2024 and Go ≥ 1.25 (`go 1.25.0` in `go.mod`, toolchain `go1.26.10`).

### Encoding (Rust, release build)

```
Message size vs FIX 4.4 (realistic payloads):
  NewOrderSingle:        FXP 86B    FIX 164B   SBE 82B    (FXP -48%)
  ExecutionReport:       FXP 91B    FIX 192B   SBE 76B    (FXP -53%)
  MarketSnapshot (5L):   FXP 134B   FIX 331B   SBE 168B   (FXP -60%, beats SBE by 20%)
  MarketDataIncremental: FXP 30B    FIX 102B   SBE 24B    (FXP -71%)

Schema v2.0 moved every enum off zero, so BUY/LIMIT/DAY/EXEC_NEW are now
encoded on the wire (proto3 omits zero values): +4 to +11 bytes per message.

Encode (pre-allocated buffer):
  FXP:  91.6ns  (10.9M/sec)
  FIX:  618.4ns (1.6M/sec)   — 6.7x slower
  SBE:  42.1ns  (23.8M/sec)  — 2.2x faster than FXP (no schema, pure memcpy)

Decode:
  FXP:  435ns   FIX: 2,988ns (6.9x slower)   SBE: 1.6ns

Roundtrip: FXP 527ns vs FIX 3,606ns (6.8x faster)
```

### End-to-End (Go gateway → Rust Core)

```
Debug-build throughput (unlimited rate):
  Concurrency=1:   1,454 orders/sec   0 errors
  Concurrency=10:  2,259 orders/sec   0 errors
  Concurrency=50:  1,449 orders/sec   0 errors  (98% fill rate)

Release-build latency (rate=25/sec, concurrency=50, orders=200, symbols=spread;
3 runs, TCP_NODELAY on all hops, default info logging):
  p50:   0.40–0.46ms   p95:  0.82–0.98ms   p99:  1.2–2.5ms   Max: 3.7–4.3ms
  9,000 fills / 10,000 orders per run, 0 errors
  Market data fan-out: p50 ≈ 40µs
```

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
│       └── fxp.proto            Protocol schema v2.1
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