# Run stack configuration contract

Baseline: `StephT0560/fxp` main `fedffdb27b9058a5a584d9a33fd96a604d8b0fd5`. Defaults below are **native binary defaults**, not Compose interpolation defaults. Empty and absent are distinguished where behavior differs. The Runner/control plane described by the uploaded Sandbox v0.1 spec does not exist in this repository.

## Process and networking contract

| Process | Entrypoint | Network and lifecycle |
| --- | --- | --- |
| Core | `fxp_core/target/release/fxp_core` | Fixed `127.0.0.1:8080`; no listen-address flag or variable. Start first. SIGINT/SIGTERM drains, syncs, then finalizes recording on success; SIGKILL does not. |
| Gateway | `bin/go_gateway` | Core destination fixed to `127.0.0.1:8080`; TCP `127.0.0.1:8082`, WS `127.0.0.1:8081` at `/ws`. Start after the core; require a real LogonAck/order acknowledgment before declaring a run ready. |
| Simulator | `bin/simday` | Dials gateway TCP, authenticates every participant, then runs for `-duration`. No server listener; seed does not make concurrent live runs deterministic. No graceful SIGTERM handler is installed: stop it before draining gateway/core. |
| Optional FIX bridge | `bin/fix_bridge` | FIX TCP `127.0.0.1:8083`; dials gateway TCP, **not core**. Start after gateway. Drain before gateway/core; persistent session store is separate from the core bundle. |

Evidence: [core bind](../../fxp_core/src/core_auth.rs#L7), [core startup](../../fxp_core/src/server.rs#L1304), [gateway main](../../go_gateway/main.go#L21), [core pool](../../go_gateway/transport/pool.go#L40), [simulator main](../../go_gateway/simulation/trading_day.go#L1032), [bridge main](../../fix_bridge/main.go#L82).

Core and gateway **must share a loopback network namespace**. Separate containers on a normal Compose bridge cannot reach the fixed core listener by service DNS; the shipped Compose files use `network_mode: service:netns`. Simday and bridge can either share that namespace or dial an explicitly configured reachable gateway endpoint. Bind any published development ports to host loopback; the core remains private. A non-loopback plaintext gateway/bridge bind requires the explicit `FXP_ALLOW_PLAINTEXT=true` development override.

## Start a recorded stack by hand

Prerequisites: Rust stable supporting edition 2024 (audit used 1.99.1), Go 1.26.10 as selected by `go.work`, protoc **25.3**, Python >=3.10 with venv, Bash, Make, OpenSSL, and optionally Node >=22 for the console. Install dependencies before testing; the runtime checks below need only localhost. From the repository root:

```bash
bash scripts/gen_proto.sh
cargo build --release --locked --manifest-path fxp_core/Cargo.toml --bin fxp_core --bin fxp-replay
mkdir -p bin
(cd go_gateway && go build -o ../bin/go_gateway . && go build -o ../bin/simday ./simulation)
(cd fix_bridge && go build -o ../bin/fix_bridge .)
python3 -m venv .venv
.venv/bin/python -m pip install -e sdk/python
```

Use a shell with no inherited deployment-specific `FXP_*` settings. Create the parent directory, but **do not create the bundle directory**. This recipe creates only new local state and disposable secrets; do not enable shell tracing or reuse the values for a hosted deployment.

```bash
umask 077
run_parent=$(mktemp -d "${TMPDIR:-/tmp}/fxp-run.XXXXXX")
export FXP_JWT_SECRET=$(openssl rand -hex 32)
export FXP_JWT_ISSUER=fxp-local-run FXP_JWT_AUDIENCE=fxp-local-gateway
export FXP_CORE_AUTH_TOKEN=$(openssl rand -hex 32)
export FXP_JWT_REPLAY_CACHE=on
export FXP_FIX_CREDENTIALS="LOCAL01:$(openssl rand -hex 32)"
export FXP_FIX_STORE_DIR="$run_parent/fix-store"
mkdir -m 700 "$FXP_FIX_STORE_DIR"
export FXP_RUN_RECORD="$run_parent/bundle"
export FXP_RUN_META_JSON='{"seed":7,"template":"quiet-book"}'

fxp_core/target/release/fxp_core >"$run_parent/core.log" 2>&1 & core_pid=$!
# Auth-aware probe, retrying startup for up to ten seconds.
ready=false
for attempt in $(seq 1 100); do
  if bash scripts/core_healthcheck.sh 2>/dev/null; then ready=true; break; fi
  kill -0 "$core_pid" || break
  sleep 0.1
done
[[ "$ready" == true ]] || { cat "$run_parent/core.log"; exit 1; }
bin/go_gateway >"$run_parent/gateway.log" 2>&1 & gateway_pid=$!
# The client waits for an authenticated LogonAck, not just a listening port.
.venv/bin/python - <<'PY'
import time
from fxp_client import Client, dev_token
for attempt in range(100):
    try:
        c = Client.connect('127.0.0.1', 8082, dev_token('readiness'), timeout=1)
        c.close()
        break
    except (ConnectionError, TimeoutError):
        time.sleep(.1)
else:
    raise SystemExit('gateway not ready: inspect gateway.log')
PY
bin/fix_bridge >"$run_parent/bridge.log" 2>&1 & bridge_pid=$! # optional
bin/simday -symbols 2 -participants 6 -duration 5 -seed 7 >"$run_parent/simday.log" 2>&1

# Prevent further producers, then close the forwarding layer, then drain core.
kill -TERM "$bridge_pid"; wait "$bridge_pid"
kill -TERM "$gateway_pid"; wait "$gateway_pid"
kill -TERM "$core_pid"; wait "$core_pid"
fxp_core/target/release/fxp-replay verify "$FXP_RUN_RECORD"
```

The equivalent supported one-shot workflow is `FXP_REPRO_PYTHON="$PWD/.venv/bin/python" make repro-demo BUG=stale-replace`; it owns startup, generated credentials, recording, shutdown and offline validation. The audit ran the stack lifecycle and SDK operations independently as well as this runner. On failure, inspect local logs and terminate **only PIDs you started**; do not attach to an existing stack or overwrite a bundle. For continuously running services, keep process supervision and stop ordering in the Runner; a shell PID or open TCP port alone is insufficient readiness evidence.

### Docker differences

`docker compose up --build -d --wait` uses the secure default stack: populate the ignored `.env` as described in [README](../../README.md), generate local certificates with `make dev-certs`, and set `FXP_CERT_GID` appropriately. Public configuration bind mounts must be readable/traversable by container UID 10001; a cloud checkout with private 0700/0600 permissions needs a local permission or mount preparation step (do not relax secret-file permissions). `make demo` is the documented loopback console workflow. The secure stack requires JWT, FIX credentials, distinct core authentication and TLS/mTLS material; `.env.example` is a template, not usable credentials.

The existing `Dockerfile` contains core/gateway/bridge, **not simday or fxp-replay**. `Dockerfile.demo` contains demo_api/simday/bridge plus the web assets, **not core/gateway/replay**; these are local build images, not evidence of published pinned release images. Neither stock Compose file passes `FXP_RUN_RECORD` or `FXP_RUN_META_JSON` to core, and simday is supervised by the demo BFF rather than a standalone Compose service. A future Runner must supply recording explicitly, a private writable parent owned by UID 10001, a unique bundle path, shared network namespace, and separate per-run settings; this audit does not add that implementation. Replay must be built natively or packaged separately.

## Core variables

Source: [server.rs:1304](../../fxp_core/src/server.rs#L1304), [recorder:423](../../fxp_core/src/reproducibility.rs#L423), [symbol configuration:90](../../fxp_core/src/symbol_config.rs#L90).

| Variable | Default | Meaning / constraints |
| --- | --- | --- |
| `FXP_RUN_RECORD` | absent: off | New bundle directory; existing directory or empty path fails startup. Parent exists and is writable. Mutually exclusive with `FXP_EVENT_LOG`. |
| `FXP_RUN_META_JSON` | `{}` | Recording caller metadata JSON object; max 4 MiB / 64 nesting levels, duplicate keys rejected. A place for image digest/template/seed, not secrets. |
| `FXP_EVENT_LOG` | absent: off | Durable engine-input journal; existing log replays. Parent must exist. Not the recording-bundle API. |
| `FXP_SYMBOL_CONFIG` | absent/empty: built-in rules | CSV path; default precision 2, tick 1 minor unit, lot/minimum 1, maximum 2147483647. Copy the same rules to bridge; recording captures normalized rules. |
| `FXP_SESSION_SCHEDULE` | absent: Continuous, no Day expiry | Strict UTC `close=HH:MM[:SS]`, or all four ordered keys `open_call,open,close_call,close`. Empty/invalid/partial settings fail startup. |
| `FXP_STP_DEFAULT` | absent/empty: `none` | `none`, `cancel-newest`, `cancel-oldest`, `cancel-both`, `decrement-and-cancel`. |
| `FXP_SYMBOLS` | absent: unrestricted | Comma-separated nonempty admission allow-list; explicitly empty is invalid. Restored symbols remain available. |
| `FXP_MAX_SYMBOLS` | absent: unlimited | Positive admitted-symbol limit; empty/zero/invalid rejected. Not a per-owner order limit. |
| `FXP_CORE_AUTH_TOKEN` | absent/empty: disabled | Shared with gateway; nonempty UTF-8 value 24–1024 bytes. Protects trusted forwarding on loopback. |
| `FXP_SHUTDOWN_TIMEOUT_SEC` | `5` | Positive integer; unsuccessful draining leaves recording incomplete. Explicit invalid/empty core setting fails. |
| `RUST_LOG` | `info` | tracing-subscriber filter. |
| `FXP_HOP_TRACE` | absent: off | Optional local diagnostic output path, separate from bundle. |
| `FXP_DHAT_FILE` | `dhat-heap.json` | Only effective for a core compiled with the optional `dhat` feature. |

`FXP_BUILD_GIT_SHA` is a **build-time** provenance override consumed by `fxp_core/build.rs:20`, not runtime run configuration. `FXP_UPDATE_*`, `FXP_PHASE1_*` and test-fixture output variables are test/golden-generation controls, not Runner inputs. Do not enable golden-update flags during validation.

## Gateway JWT contract

Sources: [auth.go:195](../../go_gateway/transport/auth.go#L195), [startup secret checks](../../go_gateway/startup/secrets.go#L13), [JWT minting for simday](../../go_gateway/test_clients/devjwt/devjwt.go#L24), [bridge minting](../../fix_bridge/gateway/client.go#L370).

| Variable | Default | Consumers and behavior |
| --- | --- | --- |
| `FXP_JWT_SECRET` | none; required >=24 bytes | Gateway validation, simday signing, bridge signing. Use a fresh random secret per run. Wrong run secret must fail authentication. |
| `FXP_JWT_ISSUER` | `fxp-dev` | Exact required `iss`; share with simday/bridge. |
| `FXP_JWT_AUDIENCE` | `fxp-gateway` | Required `aud` string or array containing this value; share with signers. |
| `FXP_JWT_KID` | empty | Current key ID; share with signers. Missing JWT kid still selects current secret. |
| `FXP_JWT_PREVIOUS_KID` / `FXP_JWT_PREVIOUS_SECRET` | absent | Gateway key rotation; both required together and previous kid must differ. Startup validates previous secret length when present. |
| `FXP_JWT_LEEWAY_SEC` | `30` | Gateway nbf tolerance in seconds. Uses positive-integer parser: **0 falls back to 30**, not zero skew. Does not provide exp leeway. |
| `FXP_JWT_REPLAY_CACHE` | off | `1/on/true/yes`, case-insensitive, enables in-memory rejection of reused `(issuer,kid,jti)` until expiry. Requires nonempty `jti`; not a persistent cache. |

Tokens must declare exactly `alg=HS256` and a configured kid, have a correct HMAC, nonempty `sub`, nonzero `exp` and `iat`, configured `iss/aud`, and acceptable optional `nbf`. Code rejects expiration when `now > exp` (equality is accepted); `iat` is required but is not checked for future issuance. JWTs authenticate identity, not workspace ownership policy implemented by a control plane; `fxp_role=operator` grants SessionControl and must not be issued to normal users. Existing simday and bridge signers include fresh jti, iat/nbf and a 24-hour expiry; the proposed short-lived hosted token endpoint does not exist.

## Gateway listeners and limits

Source: [limits.go:50](../../go_gateway/transport/limits.go#L50), [WS origins](../../go_gateway/transport/ws_server.go#L26), [mass cancel](../../go_gateway/transport/mass_cancel.go#L12).

| Variable | Default | Meaning |
| --- | --- | --- |
| `FXP_GW_TCP_ADDR` | `127.0.0.1:8082` | Native TCP bind. |
| `FXP_GW_WS_ADDR` | `127.0.0.1:8081` | Native WebSocket bind, route `/ws`. |
| `FXP_GW_WS_ALLOWED_ORIGINS` | empty | Comma-separated exact extra origins; missing Origin and valid same-origin requests are accepted. |
| `FXP_GW_MAX_FRAME_PREAUTH` | `4096` bytes | Frame bound before Logon. |
| `FXP_GW_MAX_FRAME` | `65536` bytes | Frame bound after Logon. |
| `FXP_GW_MAX_CONNS` | `1024` | Limit **per listener**, TCP and WS separately. |
| `FXP_GW_RATE` | `2000` | Integer messages/sec per connection. |
| `FXP_GW_RATE_BURST` | `4000` | Per-connection token-bucket burst. |
| `FXP_GW_HANDSHAKE_TIMEOUT_MS` | `10000` | Logon deadline. |
| `FXP_GW_CLIENT_QUEUE` | `8192` | Critical-message queue per client. |
| `FXP_GW_MD_QUEUE` | `4096` | Market-data queue per client. |
| `FXP_GW_MASS_CANCEL_TIMEOUT_SEC` | `30` | Aggregate mass-cancel completion timeout. |
| `FXP_SHUTDOWN_TIMEOUT_SEC` | `5` | Drain deadline. |

These numeric gateway settings require positive integers; empty, invalid or nonpositive values log/fall back to defaults. The pool defaults to `runtime.NumCPU()` core connections plus its dedicated market-data connection; `SetPoolSize` is a Go API, **not a Runner environment knob**. There is no native gateway `-core-addr` or pool-size flag.

## Shared Go server transport and diagnostics

Gateway and bridge use [server.go:32](../../go_gateway/tlsconfig/server.go#L32) and [diagnostics.go:70](../../go_gateway/diagnostics/diagnostics.go#L70).

| Variable | Default | Meaning |
| --- | --- | --- |
| `FXP_TLS_CERT_FILE` / `FXP_TLS_KEY_FILE` | absent | Server PEM pair; both required together to enable TLS >=1.2. |
| `FXP_TLS_CLIENT_CA_FILE` | absent | Require and verify client certificates with this PEM CA when TLS enabled. |
| `FXP_TLS_REQUIRED` | `false` | Refuse startup without TLS when true; malformed boolean fails. |
| `FXP_ALLOW_PLAINTEXT` | `false` | Explicit non-loopback plaintext development opt-in. Keep false for hosted traffic. |
| `FXP_LOG_LEVEL` | `info` | Gateway/bridge: debug/info/warn/error, invalid falls back to info. |
| `FXP_PROFILE_DIR` | absent: off | Diagnostic CPU/heap/alloc/block/mutex outputs. Writable private directory; not bundle contents. |
| `FXP_TRACE_FILE` | absent: off | Go runtime trace output. |
| `FXP_HOP_TRACE` | absent: off | Gateway hop tracing; bridge diagnostics code can open it but bridge does not emit the gateway hop stream. |

TLS material is reloaded every second; failed reload retains the last valid material. Do not put private keys, credentials, tokens, or diagnostics containing private data into committed files or report metadata.

## Simday flags and variables

Source: [trading_day.go:74](../../go_gateway/simulation/trading_day.go#L74), [flag declarations:100](../../go_gateway/simulation/trading_day.go#L100), [stdin gate:1139](../../go_gateway/simulation/trading_day.go#L1139).

| Flag / variable | Default | Meaning |
| --- | --- | --- |
| `-gateway` / `FXP_SIM_GATEWAY_ADDR` | `127.0.0.1:8082` | Flag overrides environment. |
| `-symbols` | `5` | First N symbols from built-in AAPL, MSFT, GOOGL, AMZN, NVDA, TSLA, META, JPM, `ETH/USD`, `BTC/USD` universe; clamped above 10. No arbitrary-symbol-file flag. Use positive values. |
| `-participants` | `20` | Participant count; 0 can hold an idle simulator, not a useful market. Avoid negative inputs. |
| `-duration` | `120` seconds | Simulation wall-clock lifetime; Runner enforces its own upper bound. |
| `-seed` | `0` | Zero selects current Unix nanoseconds; nonzero seeds participant RNGs, not thread scheduling. |
| `-verbose` | `false` | Individual-order logging. |
| `-opening-auction-sec` | `0` | With positive value, waits for newline on stdin after all participants authenticate; **caller must start the core opening call** before releasing it. Simulator phase labels alone do not put core into auctions. |
| `-tls` / `FXP_CLIENT_TLS` | `false` | Verified TLS to gateway. |
| `-tls-ca` / `FXP_CLIENT_TLS_CA_FILE` | system roots | CA PEM file. |
| `-tls-cert` / `FXP_CLIENT_TLS_CERT_FILE` | absent | mTLS client certificate; pair with key. |
| `-tls-key` / `FXP_CLIENT_TLS_KEY_FILE` | absent | mTLS client private key. |
| `-tls-server-name` / `FXP_CLIENT_TLS_SERVER_NAME` | dial host | Verified server name. |
| `FXP_JWT_SECRET`, `FXP_JWT_ISSUER`, `FXP_JWT_AUDIENCE`, `FXP_JWT_KID` | see JWT table | Signers must match gateway. |

TLS flags override corresponding environment values; TLS-file/name settings with TLS disabled are invalid. Simday's seed/phase flags are trusted operator inputs today, not an HTTP validation boundary. Runner must validate symbol/participant/duration ranges before executing fixed argv, never interpolate user strings into a shell command.

## Optional FIX bridge

Sources: [main.go:74](../../fix_bridge/main.go#L74), [store:191](../../fix_bridge/main.go#L191), [session defaults](../../fix_bridge/session/session.go#L143), [policy](../../fix_bridge/session/policy.go#L13), [schedule](../../fix_bridge/session/schedule.go#L97).

| Variable / flag | Default | Meaning |
| --- | --- | --- |
| `-addr` / `FXP_FIX_ADDR` | `127.0.0.1:8083` | Flag overrides bind environment. |
| `-compid` | `FXPBRIDGE` | Bridge SenderCompID; FIX clients target this ID. |
| `FXP_FIX_GATEWAY_ADDR` | `127.0.0.1:8082` | Upstream gateway TCP endpoint. |
| `FXP_FIX_CREDENTIALS` | none; required | Comma-separated `SenderCompID:password` allow-list. Supply securely per run, never commit. |
| `FXP_SYMBOL_CONFIG` | built-in rules | Same symbol scales/ticks as core. |
| `FXP_FIX_MAX_CONNS` | `1024` | Positive connection limit; invalid falls back. |
| `FXP_FIX_STORE_DIR` | native `fix_store`; image `/data/fix_store` | Durable session state; explicitly empty means in-memory. Give each run its own writable private directory. |
| `FXP_FIX_STORE_FSYNC` | `strict` | `strict`, `group`, `async`. |
| `FXP_FIX_STORE_GROUP_INTERVAL` | `10ms` effective | Group-mode fsync interval; parser passes zero by default, store applies 10ms. Go duration syntax. |
| `FXP_FIX_STORE_MAX_MESSAGES` | `100000` | Outbound resend retention. |
| `FXP_FIX_LOGON_TIMEOUT` | `10s` | Go duration syntax; test fixture defaults to 2s separately. |
| `FXP_FIX_LOGOUT_TIMEOUT` | `5s` | Logout wait; fatal close has 5s safety fallback for nonpositive setting. |
| `FXP_FIX_MAX_LATENCY` | `120s` | SendingTime/PossDup timing checks. |
| `FXP_FIX_COMPAT` | `default` | `quickfix` enables silent failed Logon, bounded fatal Logout wait and silent heartbeat expiry close. |
| `FXP_FIX_SILENT_LOGON_FAILURES` | preset (`false` default) | Optional explicit bool overrides preset. |
| `FXP_FIX_WAIT_PEER_LOGOUT` | preset (`false` default) | Optional explicit bool overrides preset. |
| `FXP_FIX_START_TIME` / `FXP_FIX_END_TIME` | absent: no schedule | Supply both `HH:MM:SS` for daily/weekly session window. |
| `FXP_FIX_START_DAY` / `FXP_FIX_END_DAY` | absent: daily | Supply both for weekly schedule (`Sun`…`Sat`). |
| `FXP_FIX_TIME_ZONE` | `UTC` | IANA timezone for FIX schedule. |
| `FXP_FIX_DICTIONARY_STRICT` | `false` | Strict dictionary policy; normal validation still applies when false. |
| `FXP_FIX_RESET_ON_LOGON` | `false` | Authenticated reset requires inbound sequence 1. |
| `FXP_FIX_RESET_ON_LOGOUT` | `false` | Reset session after Logout. |
| `FXP_FIX_RESET_ON_DISCONNECT` | `false` | Reset after logged-on disconnect. |
| `FXP_GATEWAY_TLS` | `false` | Verified TLS for bridge→gateway. |
| `FXP_GATEWAY_TLS_CA_FILE` | system roots | Gateway trust CA. |
| `FXP_GATEWAY_TLS_CERT_FILE` / `FXP_GATEWAY_TLS_KEY_FILE` | absent | Optional paired upstream mTLS identity. |
| `FXP_GATEWAY_TLS_SERVER_NAME` | dial host | Verified upstream name. |
| `FXP_JWT_SECRET`, `FXP_JWT_ISSUER`, `FXP_JWT_AUDIENCE`, `FXP_JWT_KID` | see JWT table | Bridge generates its upstream token. |
| `FXP_SHUTDOWN_TIMEOUT_SEC`, server TLS/logging/profile variables | see shared tables | Same shared code/defaults as gateway. |
| `-test-fixture` / `FXP_FIX_TEST_FIXTURE` | disabled | Test-only compiled QuickFIX echo adapter; rejected by normal builds. Never configure in Runner. |

FIX heartbeat interval is negotiated with Logon `108` (initial session default 30s), not an environment setting. Bridge JWT subjects derive from authenticated FIX SenderCompID. `FXP_CORE_AUTH_TOKEN` is used by core/gateway, not to authenticate bridge upstream; bridge startup still validates it if inherited through the common startup validator.

## Runner boundaries

Start processes with fixed executable paths and separate argv/env fields; no shell interpolation of tokens, symbols, paths, uploaded filenames or metadata. Treat all bundle files as private and untrusted, allow only run-owned generated paths, enforce upload/expanded-size/file-count/time/memory limits before replay, and run the worker without credentials or outbound network access. The report command has some input/output bounds, but that does not establish bounded ingestion for every replay command or provide archive extraction security. Do not infer a per-run resource quota, retention policy, hosted isolation guarantee or 20-run host capacity from the native process limits above.
