# Local FXP Console BFF

Run `make demo` from the repository root with Docker Compose **2.24.4+** and
Python 3 installed. It creates a private, gitignored `.env.demo` with a random
32-byte JWT secret and starts the dev + demo profiles. Open
**http://127.0.0.1:3000**. The BFF serves the console from `web/dist` (see
[Console UI](#console-ui)); without a build it shows a small placeholder. `make demo-down` stops the
stack without deleting the private env file or volumes. Remove `.env.demo` while
the stack is stopped to generate new credentials on the next start.

Only **127.0.0.1:3000** is published. Core, gateway TCP/WS and FIX ports are not
published by this overlay; the FIX bridge is disabled unless the `fix` profile
is explicitly added. BFF, simulator, core and gateway share the core container's
network namespace and use loopback for upstream connections. This is a local
demo, with plaintext traffic and no user authentication beyond choosing a fixed
demo identity. Do not expose it as a hosted service.

The equivalent Compose command (after `make demo` has created `.env.demo`) is:

```sh
docker compose --env-file .env.demo -p fxp-demo \
  -f docker-compose.dev.yml -f docker-compose.demo.yml \
  --profile dev --profile demo up --build --detach --wait
```

The demo image builds `web/` with `npm ci && npm run build` if its package manifest
is present. It contains `demo_api`, `simday`, the owned `fix_bridge` executable,
built UI assets and symbol config. Hosted multiplexing, TLS termination and
backend metrics remain out of scope.

For a native run, generate bindings (`bash scripts/gen_proto.sh`), start core and
gateway, build `go build -o bin/simday ./go_gateway/simulation`, and run
`go run ./demo_api` from the root with `FXP_JWT_SECRET` in the environment. It
must match the gateway, contain at least 24 bytes, and differ from the development
constant. Set matching `FXP_JWT_ISSUER`, `FXP_JWT_AUDIENCE`, and optional
`FXP_JWT_KID` on all processes. Use `FXP_SYMBOL_CONFIG=config/symbols.demo.csv`
on all three. The shared parser returns explicit rules plus default rules for
the simulator's ten default symbols, sorted by name. Unlisted symbols are still
accepted by the core under its default rules; this endpoint is a demo list,
not a list of all possible symbols.

| Environment variable | Default | Meaning |
|---|---|---|
| `FXP_DEMO_ADDR` | `127.0.0.1:3000` | BFF HTTP listen address; literal loopback unless explicitly allowed |
| `FXP_DEMO_ORIGIN` | `http://127.0.0.1:3000` | Exact public Origin and Host accepted; no path; use the printed URL |
| `FXP_ALLOW_PLAINTEXT` | `false` | Explicitly permit a non-loopback listener; demo Compose enables this inside its namespace |
| `FXP_DEMO_GATEWAY_WS_URL` | `ws://127.0.0.1:8081/ws` | Private upstream WebSocket URL; no credentials/query |
| `FXP_DEMO_STATIC_DIR` | `web/dist` | Built assets; also overridable with `-static-dir` |
| `FXP_DEMO_FIX_BINARY` | `bin/fix_bridge` | Trusted subprocess for FIX recovery scenarios; demo image sets `/usr/local/bin/fix_bridge` |
| `FXP_DEMO_SIM_BINARY` | `bin/simday` | Trusted simulator executable, never supplied by an API caller |
| `FXP_SIM_GATEWAY_ADDR` | `127.0.0.1:8082` | Simulator upstream TCP address |
| `FXP_DEMO_TOKEN_TTL_SEC` | `900` | Token lifetime, integer 1–900 seconds |
| `FXP_DEMO_TOKEN_RATE_PER_MIN` | `10` | Token attempts per IP per fixed one-minute window, 1–120 |
| `FXP_JWT_SECRET` | required | Environment-only secret, >=24 bytes, non-default |
| `FXP_JWT_ISSUER` / `FXP_JWT_AUDIENCE` | `fxp-dev` / `fxp-gateway` | Shared gateway JWT validation claims |
| `FXP_JWT_KID` | unset | Shared optional key id |
| `FXP_SYMBOL_CONFIG` | unset | Shared venue CSV; demo Compose fixes `/etc/fxp/config/symbols.demo.csv` |
| `FXP_CLIENT_TLS*` | unset | Existing simulator TLS settings; passed only to the simulator if configured (see [simulator docs](../simulator.md)) |

`FXP_DEMO_GATEWAY_WS_URL` supports local plaintext `ws` only; secure hosted
upstream transport and browser TLS termination are intentionally out of scope.

| Route | Contract |
|---|---|
| `GET /healthz` | `{"status":"ok"}`: BFF liveness, not upstream readiness |
| `GET /api/users` | Fixed ids: `alice`, `bob`, `market-maker` |
| `GET /api/symbols` | Sorted objects with `symbol`, `price_scale`, `tick_size` (decimal **string** for int64 precision), `lot_size`, `min_quantity`, `max_quantity` |
| `POST /api/token` | JSON `{"user":"alice"}` -> `{"token":"...","expires_in":900}`; subject `demo:alice`; caller keeps token in memory only |
| `GET /ws` | Same-origin binary WebSocket proxy; client sends JWT Logon as first protobuf message |
| `POST /api/sim/start` | JSON `{"seed":7,"symbols":2,"participants":4,"duration":5}`; symbols 1–10, participants 1–50, duration 1–300 seconds; seed is signed int64 (0 uses current time) |
| `POST /api/sim/stop` | Stops/reaps current simulator, idempotent |
| `GET /api/sim/status` | `running`, `params`, `started_at`, `exit_code`, `logs` (tail <=32 KiB) |

POST JSON is limited to 4 KiB and rejects unknown fields and trailing objects.
Foreign browser Origins are refused on POST endpoints as well as `/ws`. Missing
Origin is allowed for native HTTP tools, but `/ws` requires the exact configured
Origin. The configured Host is pinned to prevent DNS rebinding; forwarded host/IP
headers are ignored. IP rate-limit state is bounded to 4096 entries, expiring
after one minute. A full active table refuses new addresses.

The WebSocket proxy deliberately creates a fresh upstream handshake with **no
browser Origin**, cookies or Authorization headers. Both directions accept only
binary messages, max 1 MiB, preserving bytes and message boundaries (no TCP
length prefix). Close codes propagate; either disconnect closes both sockets,
and shutdown closes all hijacked connections. Writes have bounded deadlines.

JWT minting reuses the shared devjwt signer, with fresh jti, iat/nbf, issuer,
audience, optional kid and <=15 minute expiry. The JWT signing secret/core token
never enters browser responses. Tokens are intentionally returned only by the
token endpoint and must stay in UI memory. No request bodies, tokens or
credentials are logged. CSP permits same-origin scripts/styles/connections only;
no inline scripts or styles. Unknown assets return 404; extensionless UI routes
fall back to index.html. Static file access is rooted with `os.OpenRoot`.

Only one simulator runs at a time. Its fixed executable receives numeric argv
only, the secret through an allow-listed environment, and never the core token.
Stop, BFF shutdown, and a hard deadline of `duration + 30 seconds` kill its process
group and reap it. Output is captured, never forwarded to server logs; log lines
containing the secret or a JWT are redacted before storage, and oversize lines
are discarded. This is a bounded diagnostic tail, not durable audit logging.
Live seeded simulation retains the timing limits documented in [simulator docs](../simulator.md).

Core subscription constraint: a subscription to an unknown (not yet created)
book is ignored by the current core. Subscribe after the simulator has placed
its first orders, or create passive orders before subscribing. The integration
fixture explicitly admits both test symbols first; the BFF does not synthesize
orders or alter market-data messages.

Validation:

```sh
(cd demo_api && go vet ./... && go test -race -count=1 ./...)
bash scripts/demo_api_smoke.sh
make demo
python3 scripts/demo_compose_check.py
make demo-down
```

The integration runner requires free core/gateway ports 8080–8082. It builds and
runs a real core, gateway and BFF, verifies token -> WS LogonAck, a foreign-Origin
403, independent Rust public trades, simulator lifecycle/shutdown, and absence
of secrets in responses, captured logs and process argv. The `demo-api` CI job
runs these checks and a real Compose build; it is required by `ci-success`.

## Console UI

`web/` is the browser console (v0.1a): React 18, strict TypeScript, Vite,
Tailwind, Zustand and Vitest; protobuf-es bindings keep every int64/uint64 as
`bigint`. It is local-only, read-only market view: demo-user login, connection
and trading-phase status bar, auction banner (indicative price/volume and
imbalance during calls), symbol list with last price, 10-level depth ladder,
public trade tape with a per-symbol **tape gap** counter, last price and session
volume, a `lightweight-charts` price line, and simulator start/stop with a seed.

Data flow: `POST /api/token` (JWT kept in memory only) -> same-origin `/ws` ->
`Logon` as the first binary frame -> on accepted `LogonAck`, one
`MarketDataRequest` for every symbol. Symbols still without a snapshot are
re-requested every 2 s (the core ignores requests for books it has not created
yet). A rejected `LogonAck` stops the client and shows the reason; any other
close marks books unsynced and reconnects with exponential backoff (0.5 s
doubling to 10 s, plus jitter) and a fresh token.

Book semantics: a snapshot replaces the book; `MarketOrderUpdate` is an
order-level delta (`update_type` 0 adds `quantity` at the level, 1 fill and 2
cancel remove it). Incrementals before the first snapshot are dropped, deltas
that would go negative are clamped and counted as anomalies, and a crossed
book is labelled rather than hidden. Trades use the per-symbol `seq`: a jump
increments the visible gap counter (gaps are never hidden), duplicates and late
prints are dropped so volume is not double counted. Store notifications are
coalesced to one per animation frame, so a burst of incrementals costs one
render.

The core now also publishes the symbol's current `TradingSessionStatus` right
after each subscription snapshot, so late joiners learn the phase, and the
gateway sends every `MarketDataRequest` on one core connection so it is a
single core subscriber (spreading them over the pool duplicated every update).

Dev loop (Node 22):

```bash
cd web && npm ci
npm run dev          # Vite on :5173, proxies /api and /ws to the BFF on :3000
npm run gen          # regenerate src/gen from fxp_core/proto (buf + protoc-gen-es)
npm run typecheck && npm test
npm run build && npm run size   # gzip JS+CSS budget: 160 kB (FXP_CONSOLE_BUDGET_KB)
bash scripts/check-bundle-secrets.sh dist
npm run e2e          # Playwright smoke against FXP_CONSOLE_URL (default :3000)
```

`npm run dev` needs the BFF's `FXP_DEMO_ORIGIN` to match the page origin
(`http://127.0.0.1:5173`) because `/ws` is same-origin checked; the default
flow is `npm run build` and the BFF serving `web/dist`. Decoder tests use
golden frames encoded by the Go gateway's protobuf package:
`cd go_gateway && go run ./cmd/console_golden ../web/src/testdata/golden`.
CI job `console-e2e` checks generated code/fixtures are current, runs
typecheck, Vitest (decoders, book/tape stores, gaps, crossed and out-of-order
updates, a 2,000-update replay timing test), the bundle budget and secret scan
(built and served bundle), then brings up `make demo` and runs the Playwright
smoke with trace/video artifacts.

## Trading (v0.1b)

**Order ticket.** Limit, market, stop and stop-limit orders; IOC, FOK, GTC,
Day, GTD (the expiry date sets `expire_date`, UTC) and at-open; iceberg display
quantity (`display_quantity`, MaxFloor); STP mode and group. Prices are parsed
from decimal text into minor units with `bigint` and checked against the
symbol's `price_scale`, `tick_size`, `lot_size`, `min_quantity` and
`max_quantity` from `/api/symbols`. These checks only save a round trip: the
core remains authoritative, and its rejects are shown with their reasons. Each
order gets a fresh client order id, and the ticket allows one order in flight
until its first report arrives (or 5 s pass).

**Blotter.** Built from the user's private ExecutionReports. Working and
completed tabs show cum, leaves and average price. Reports are de-duplicated by
execution id; cumulative quantity never decreases; a terminal order (filled,
canceled, rejected, expired, replaced) only moves to another terminal state, so
reordered reports such as an IOC remainder cancel ahead of its fill cannot
resurrect it. EXEC_RESTATED (self-trade prevention) marks the row restated. A
rejected cancel or replace (`orig_order_id` set) leaves the order unchanged and
shows the reason on the row. Cancel-all sends `OrderMassCancelRequest` for all
symbols or the selected one and shows the `OrderMassCancelReport`. Own fills
are listed separately from the public tape; the exec-report tab logs every
report and request.

**Reconnect.** When the socket drops the blotter is marked as resyncing. After
LogonAck the console sends `OrderStatusRequest` (all orders) and replaces the
blotter with the response batches. Execution reports carry no limit price, so
prices are kept for orders entered in this tab and shown as "—" after a page
refresh.

**Opening auction.** The core takes its phases from `FXP_SESSION_SCHEDULE`
only. To reach an auction on demand, `POST /api/sim/start` accepts
`opening_auction_sec` (5–120, shorter than `duration`). The BFF then connects to
the gateway with a one-minute operator token (`fxp_role=operator`) and sends
`SessionControl`: every live and configured symbol enters an opening call now
and uncrosses after that many seconds, printing auction trades on the tape. The
simulator starts in its opening-auction phase (at-open orders, no IOC) and
trades continuously afterwards. The gateway rejects `SessionControl` from any
other token. Demo users can trigger it through the BFF; this is a local demo
control, not an operator console.

`DEMO` is configured but never traded by the simulator, which makes it the
place to try fills between two users. The 90-second walkthrough is in
[demo-script.md](demo-script.md).


## Latency panel (v0.2)

Select **Latency** after login. `GET /api/bench` serves the checked-in
`benchmarks/console.json` read-only, validated against the
[console evidence schema](../benchmark-methodology.md#read-only-console-evidence-schema-v1).
The fixed projection and its source digests are verified with
`python3 scripts/export_console_bench.py --check`; no workload can be launched
from the UI. Native runs use the repository root (or `demo_api/`) as their
working directory; the demo image includes the evidence and report under `/app`.

Recharts shows **both intended-time ACK and post-send ACK**, p50 and p99, for
one selected scenario. Error bars and the numeric table show min/max of the
per-repetition statistics alongside their median. The environment card gives
hardware, network, open/closed mode, pacer, versions, CPU pinning/quota, measured
git SHA and artifact digest. **Read profile-r4.md** opens the checked-in report
through `GET /api/bench/profile-r4`.

The pacer contributes to intended-time latency; post-send includes construction,
serialization, blocked writes, server hops and decoding. These are historical
plaintext **loopback** results, never WAN/TLS numbers. Capacity means **tested
knee** for the specified finite workload, never an SLO. No cross-protocol speed
ranking is supported. Missing, malformed or inconsistent data shows an explicit
empty state and no chart. The initial bundle retains its 160 KiB gzip budget;
the on-demand latency chart has a separate 120 KiB budget (checked using Vite's
manifest, including static dependencies).

## Protocol inspector (v0.2)

Select **Protocol inspector**. The browser taps every sent/received binary WS
application frame, including Logon, subscriptions, order/status requests and
responses. The latest 200 sanitized records stay in memory only (frames over 64 KiB
retain metadata only) and clear on
logout/user change. Filter by message type, symbol text and direction; select a
row for decoded JSON, UTC capture timestamp, monotonic browser timestamp and raw
hex. These timestamps describe browser observation, not server-hop timing.

Logon JWTs and recursively nested token/credential-like keys and values are
redacted **before entering inspector history**. Logon hex is always withheld.
Unknown/noncanonical protobuf bytes, malformed frames and frames containing
redactions have no stored raw bytes or comparison payload. Safe known messages
retain their exact original hex; int64 fields remain decimal strings in JSON.
The inspector batches notifications with the existing animation-frame scheduler.

For a captured new order, **FXP vs FIX for this order** posts its exact FXP
WS envelope as `{ "frame": "<base64>" }` to `POST /api/inspect/fix`. The BFF calls
`fix_bridge/translator.RenderNewOrderFIX`, which reuses the bridge's authoritative
inbound enum mappings and price rules. It does not send an order. The response
contains `fix` (SOH displayed as `|`), `fxp_bytes`, `fix_bytes` and an explanatory
`note`. FIX uses an illustrative client header (`CONSOLE` → `FXPBRIDGE`, sequence
1), with the captured order timestamp. Byte counts include each application
envelope and exclude WS/TCP/TLS overhead. FXP-only routing/header extensions have
no FIX equivalent; these sizes do not establish protocol speed or full semantic
equivalence. Explicit STP NONE has no mapping in the current bridge and returns
422, as do other unsupported orders; no approximate representation is shown.
The endpoint accepts only bounded new-order frames, rejects unknown fields and
FIX delimiter injection, and uses the same Origin/Host controls as other POSTs.

Validation: Vitest covers chart transforms and credential redaction with
independent Go protobuf goldens; Go endpoint/translator tests use the fixed FIX
messages in `fix_bridge/translator/testdata/order_preview.json` (independent
Python field/checksum construction). `console-e2e` automatically includes
`web/e2e/inspector.spec.ts` for real WS capture, filters, hex, translation,
provenance, spread and malformed-data empty states. An existing system Chromium
can be selected for local tests with `FXP_CONSOLE_CHROMIUM=/usr/bin/chromium`.
No `/metrics` tiles, benchmark runner, hosted mode or admin API is added.

## Scenarios (v0.2)

The **Scenarios** page runs six seeded, self-checking demonstrations through
server-side gateway sessions. The browser watches the narration timeline and
highlighted ladder/tape/observer blotter; one run at a time, with stop and
cleanup. See [scenarios.md](scenarios.md) for each assertion, the SSE/API contract,
determinism boundaries, symbol-admission settings and isolated FIX recovery.

### Own orders in depth

A blue quantity marker shows the sum of your working leaves at a known price,
separately on bids and asks. Only prices entered in this console are known;
status reports do not supply limit prices. Footers count working orders outside
the displayed ten levels, and working orders whose price is unknown. No price
is inferred after a reload.

### Observed auction countdown

Simulator opening calls show an estimate from the browser's observed
OPENING_CALL arrival plus the simulator's configured duration. It ticks once
per second and clamps to “uncrossing…”. The tooltip identifies this as an
observed-arrival estimate, not a scheduled core timestamp. Duplicate status
messages do not restart it. Unknown durations, scenario calls and late-join
snapshots without an attributable start show no countdown. Quick demo and
Start simulator confirm their own run metadata so a fresh first call can be
attributed without guessing. One shared interval serves every banner and is
cleaned up when unused.

### First run and Quick demo

**Quick demo** logs in as Alice, waits for the live gateway connection, selects
AAPL and starts the usual seeded simulator defaults with a shorter ten-second
opening call. An already-running simulator is treated as success. Plain Alice
and Bob login buttons remain available; the market-maker service identity is
kept out of this login list. Before any snapshot or print arrives, depth and tape
explain how to start receiving market data.
