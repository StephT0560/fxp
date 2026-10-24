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
| `NewOrderSingle` size | 82 bytes | 164 bytes | **50% smaller** |
| `ExecutionReport` size | 84 bytes | 192 bytes | **56% smaller** |
| `MarketDataSnapshot` (5L) | 123 bytes | 331 bytes | **63% smaller, beats SBE** |
| Encode throughput | 10.9M msg/sec | 1.6M msg/sec | **6.7x faster** |
| Decode throughput | 2.3M msg/sec | 335K msg/sec | **6.9x faster** |
| Round-trip (encode+decode) | 527 ns | 3,606 ns | **6.8x faster** |

End-to-end (order → ExecutionReport): **p50 21.8ms** at 50 concurrent senders in the release-build benchmark (Rust `--release`, Go `go build`); the debug-build measurement is 17ms.

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

## Proto Schema (v2.0)

Full institutional-grade message coverage:

```
NewOrderSingle          — order entry with stop_price, expire_date, account
OrderCancelRequest      — cancel working order
OrderCancelReplaceRequest — amend price/quantity
ExecutionReport         — lifecycle reports with leaves_qty, avg_px, cum_qty
MarketDataSnapshot      — full order book depth
MarketDataIncrementalUpdate — level changes
TradeCaptureReport      — trade print feed
ErrorMessage            — structured error responses
```

Version 2.0 shifts all existing enum numbers by one and is wire-breaking against 1.x. All enums use a per-enum `*_UNSPECIFIED = 0` sentinel; existing named values start at 1. Unknown and unspecified order-entry enums are rejected. In an `OrderCancelReplaceRequest`, `new_time_in_force = TIME_IN_FORCE_UNSPECIFIED (0)` means keep the current value.

**TimeInForce:** `TIME_IN_FORCE_UNSPECIFIED=0` · `DAY=1` · `GTC=2` · `IOC=3` · `FOK=4` · `AT_OPEN=5` · `AT_CLOSE=6` · `GTD=7`

**ExecutionType:** EXEC_NEW · EXEC_FILLED · EXEC_CANCELED · EXEC_REPLACED · EXEC_REJECTED · EXEC_EXPIRED

Custom firm extensions: field numbers 1000–1999 reserved per message.

---

## Quick Start

### Prerequisites

- Rust toolchain (`rustup`)
- Go 1.25.0+ (the modules select Go 1.26.10)
- `protoc` (the generation script installs the pinned `protoc-gen-go` plugin when needed)

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
# 🚀 FXP Core running on 127.0.0.1:8080
```

### 3. Start Go Gateway

```bash
cd go_gateway
export FXP_JWT_SECRET="dev-secret-change-in-production"
go run main.go
# ✅ TCP Server started on 127.0.0.1:8082
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

## Tests

Counts observed at this revision:

| Suite | Test count |
|---|---:|
| `fxp_core` (`cargo test`) | 52 unit tests |
| `go_gateway/transport` (`go test -v`) | 17 top-level tests |
| `fix_bridge` (`go test -v ./...`) | 53 top-level tests |
| Python FIX E2E client | 11 scenarios, 27 reported checks |

The Gateway count is from its `transport` test package; package-wide `go test` also reports the existing `benchmark.go:479` vet warning.

---

## Benchmark Results

Full methodology and results: [`Benchmark.md`](Benchmark.md)

**Benchmark environment:** Windows 11, 8 logical cores, Rust 1.85.0, Go 1.24.1. The E2E figures below distinguish debug and release builds.

### Encoding (Rust, release build)

```
Message size vs FIX 4.4 (realistic payloads):
  NewOrderSingle:        FXP 82B    FIX 164B   SBE 82B    (FXP ties SBE, both -50%)
  ExecutionReport:       FXP 75B    FIX 192B   SBE 76B    (FXP -61%)
  MarketSnapshot (5L):   FXP 123B   FIX 331B   SBE 168B   (FXP -63%, beats SBE by 27%)
  MarketDataIncremental: FXP 30B    FIX 102B   SBE 24B    (FXP -71%)

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

Release-build latency (rate=25/sec, concurrency=50, symbols=spread):
  p50:   21.8ms   p95:  313ms     p99:  409ms    Max: 803ms
  Market data fan-out: p95 < 1ms
```

---

## Repository Structure

```
fxp/
├── fxp_core/                    Rust matching engine
│   ├── src/
│   │   ├── server.rs            TCP server, per-symbol dispatch, stop trigger logic
│   │   ├── lib.rs               Unit tests (52 tests)
│   │   └── order_book/
│   │       ├── order.rs         Order struct, OrderType, TimeInForce enums
│   │       ├── order_book.rs    BTreeMap price-time priority book
│   │       ├── matcher.rs       Aggressive matching: Market/IOC/FOK/Stop
│   │       └── execution_report.rs
│   ├── benches/
│   │   └── encoding_benchmark.rs  Criterion benchmarks vs FIX and SBE
│   └── proto/
│       └── fxp.proto            Protocol schema v2.0
│
├── go_gateway/                  Go gateway
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
│   └── gen_proto.sh             Generate protobuf bindings
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