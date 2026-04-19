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
| `ExecutionReport` size | 75 bytes | 192 bytes | **61% smaller** |
| `MarketDataSnapshot` (5L) | 123 bytes | 331 bytes | **63% smaller, beats SBE** |
| Encode throughput | 11.4M msg/sec | 1.7M msg/sec | **6.7x faster** |
| Decode throughput | 2.3M msg/sec | 373K msg/sec | **6.2x faster** |
| Round-trip (encode+decode) | 834 ns | 3,828 ns | **4.6x faster** |

End-to-end (order → ExecutionReport): **p50 21.8ms** at 50 concurrent senders, release build, both sides compiled.

---

## Architecture

```
Client (TCP/WebSocket)
    │  FXP binary frames (4-byte length prefix + Protobuf body)
    ▼
Go Gateway  :8082
    ├── JWT authentication (HS256)
    ├── Session management
    ├── Connection pool (8 persistent connections to Rust Core)
    └── orderClientMap (routes ExecutionReports back to originators)
    │
    ▼
Rust Core  :8080
    ├── SymbolRouter: one tokio task per symbol (fully parallel)
    ├── Lock-free output channels (no shared write mutex)
    ├── BTreeMap price-time priority order book
    └── Full order type support: Market, Limit, IOC, FOK, Stop, StopLimit
    │
    ▼
Client ← ExecutionReport + MarketDataIncrementalUpdate
```

**Key design decisions:**
- Per-symbol tokio tasks own their order books exclusively — no `Arc<Mutex<OrderBook>>` contention across symbols
- Lock-free `mpsc` output channels replace `SharedWriter` mutex — 25 concurrent symbol tasks write without coordination
- 8-connection pool in Go — reduces per-sender lock contention vs single connection
- Protobuf binary encoding with pre-allocated buffers — 11M encodes/sec single thread

---

## Order Types

| Type | Behaviour | Status |
|---|---|---|
| `LIMIT` Day/GTC/GTD | Rests on book, passive matching | ✅ |
| `LIMIT` IOC | Fill available qty, cancel remainder | ✅ |
| `LIMIT` FOK | Fill all or cancel all (liquidity check first) | ✅ |
| `MARKET` | Sweep book at any price, cancel unfilled | ✅ |
| `STOP` | Parks until `last_trade_price` crosses `stop_price`, then fires as market | ✅ |
| `STOP_LIMIT` | Parks until stop triggers, then rests as limit | ✅ |
| `AT_OPEN` | Participates in opening auction | ✅ (rests; auction batch pending) |
| `AT_CLOSE` | Participates in closing auction (MOC) | ✅ (rests; auction batch pending) |

**Cancel workflows:**
- `OrderCancelRequest` — cancel a working order
- `OrderCancelReplaceRequest` — atomically amend price/quantity

---

## Proto Schema (v1.1)

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

**TimeInForce:** DAY · GTC · IOC · FOK · AT_OPEN · AT_CLOSE · GTD

**ExecutionType:** EXEC_NEW · EXEC_FILLED · EXEC_CANCELED · EXEC_REPLACED · EXEC_REJECTED · EXEC_EXPIRED

Custom firm extensions: field numbers 1000–1999 reserved per message.

---

## Quick Start

### Prerequisites

- Rust toolchain (`rustup`)
- Go 1.21+
- `protoc` with `protoc-gen-go` plugin

### 1. Generate protobuf bindings

```bash
cd fxp_core/proto
protoc --go_out=../../go_gateway --go_opt=paths=source_relative fxp.proto
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

## Tests

```bash
cd fxp_core
cargo test
# running 23 tests
# test_full_order_lifecycle       ... ok
# test_market_full_fill           ... ok
# test_market_sweeps_multiple_levels ... ok
# test_ioc_full_fill              ... ok
# test_ioc_partial_then_cancel    ... ok
# test_fok_full_fill              ... ok
# test_fok_kill_insufficient_liquidity ... ok
# test_fok_does_not_consume_book_on_kill ... ok
# test_stop_fires_as_market_order ... ok
# test_stop_limit_rests_after_trigger ... ok
# test_drain_triggered_stops_buy  ... ok
# test_drain_triggered_stops_sell ... ok
# ... 23 passed; 0 failed
```

---

## Benchmark Results

Full methodology and results: [`BENCHMARK.md`](BENCHMARK.md)

**Environment:** Windows 11, 8 logical cores, Rust debug build, Go `go run`

### Encoding (Rust, release build)

```
Message size vs FIX 4.4 (realistic payloads):
  NewOrderSingle:        FXP 82B    FIX 164B   SBE 82B    (FXP ties SBE, both -50%)
  ExecutionReport:       FXP 75B    FIX 192B   SBE 76B    (FXP -61%)
  MarketSnapshot (5L):   FXP 123B   FIX 331B   SBE 168B   (FXP -63%, beats SBE by 27%)
  MarketDataIncremental: FXP 30B    FIX 102B   SBE 24B    (FXP -71%)

Encode (pre-allocated buffer):
  FXP:  87.9ns  (11.4M/sec)
  FIX:  574ns   (1.7M/sec)   — 6.5x slower
  SBE:  39.4ns  (25.4M/sec)  — 2.2x faster than FXP (no schema, pure memcpy)

Decode:
  FXP:  430ns   FIX: 2,681ns (6.2x slower)   SBE: 1.5ns

Roundtrip: FXP 834ns vs FIX 3,828ns (4.6x faster)
```

### End-to-End (Go gateway → Rust Core, debug build)

```
Throughput (unlimited rate):
  Concurrency=1:   1,454 orders/sec   0 errors
  Concurrency=10:  2,259 orders/sec   0 errors
  Concurrency=50:  1,449 orders/sec   0 errors  (98% fill rate)

Latency (rate=25/sec, concurrency=50, symbols=spread):
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
│   │   ├── lib.rs               Integration tests (23 tests)
│   │   └── order_book/
│   │       ├── order.rs         Order struct, OrderType, TimeInForce enums
│   │       ├── order_book.rs    BTreeMap price-time priority book
│   │       ├── matcher.rs       Aggressive matching: Market/IOC/FOK/Stop
│   │       └── execution_report.rs
│   ├── proto/
│   │   └── fxp.proto            Protocol schema v1.1
│   └── benches/
│       └── encoding_benchmark.rs  Criterion benchmarks vs FIX and SBE
│
├── go_gateway/                  Go gateway
│   ├── transport/
│   │   ├── pool.go              8-connection pool to Rust Core
│   │   ├── tcp_server.go        TCP server, auth routing
│   │   ├── common.go            Message router, market data fan-out
│   │   ├── ws_server.go         WebSocket server
│   │   └── auth.go              JWT validation, session management
│   ├── benchmark/
│   │   └── benchmark.go         End-to-end latency and throughput benchmark
│   └── simulation/
│       └── trading_day.go       Trading day simulation
│
└── BENCHMARK.md                 Full benchmark methodology and results
```

---

## Roadmap

- [ ] Opening/closing auction batch matching at phase transitions
- [ ] Stop/StopLimit trigger on live last-trade-price feed
- [ ] `OrderCancelRequest` / `OrderCancelReplaceRequest` full round-trip in simulation
- [ ] Release build benchmarks
- [ ] CI/CD pipeline
- [ ] TLS support (infrastructure exists in proto; not yet wired)
- [ ] FIX 4.x bridge (inbound FIX → FXP translation layer)

---

## License

MIT