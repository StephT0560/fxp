# Local FXP Console BFF

Run `make demo` from the repository root with Docker Compose **2.24.4+** and
Python 3 installed. It creates a private, gitignored `.env.demo` with a random
32-byte JWT secret and starts the dev + demo profiles. Open
**http://127.0.0.1:3000**. Until `web/dist/index.html` exists the server shows a
small placeholder; UI code is a separate work item. `make demo-down` stops the
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
is present. It always contains `demo_api`, `simday`, and symbol config. No UI
sources, hosted multiplexing, TLS termination, or metrics are added in this PR.

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
