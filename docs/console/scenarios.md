# Seeded, narrated scenarios (v0.2)

Run `make demo`, log in, stop the normal simulator, and select **Scenarios**.
Choose an integer seed and a scenario. The browser only observes: separate,
run-scoped Alice/Bob identities trade through real gateway TCP sessions in the
BFF. The ladder, tape, auction banner and observer blotter reuse the console's
existing stores. The related panel is highlighted as each step runs. Every step
shows elapsed time, its assertion result, and a precise failure if an observed
report, quantity, price, phase or sequence disagrees with the fixture.

One scenario runs at a time. Starting the simulator during a scenario, or a
scenario during the simulator, returns 409. **Stop scenario** cancels it and
waits for cleanup. Leaving the page does not stop it: returning reconnects and
replays narration. The final status is published only after cleanup. A failed
assertion stops subsequent trading steps; cleanup still runs. The runner does
not silently retry failed assertions or turn failures into successful demos.

## Fixtures and assertions

All prices below are integer minor units at scale 2. The seed selects a base
price `P` and quantity `Q` (5–10). Each run gets a different, unlisted
`SCN-<random-run-id>` symbol and distinct owner identities. IDs and elapsed
wall-clock times intentionally differ; same-seed **asserted outcomes** do not.
Setup admits the book with a passive order, subscribes an observer, removes the
admission order and establishes an empty snapshot barrier. No normal demo
symbol receives scenario orders.

| Scenario | Real sequence and assertions |
|---|---|
| Opening auction uncross | A symbol-scoped operator control starts a four-second opening call and must report exactly one affected symbol. Equal-price, equal-quantity at-open buys/sells must rest crossed without a print. The uncross must produce exactly one public auction print at `P` for `Q`, full auction reports to both users, and an empty book. |
| Stop cascade | Rest one ask at each of `P`, `P+1`, `P+2`; park buy stops at `P` and `P+1`. One buy at `P` must produce three prints in per-symbol sequence 1,2,3 at those prices, with stop-1 then stop-2 filling exactly once, and no residual book. Each successive print elects the next stop. |
| STP modes side by side | Reset the same fresh book between five identical fixtures: one owner's resting buy `Q` and aggressing sell `Q-1`, at `P`. NONE prints once and leaves bid 1; CANCEL_NEWEST cancels the aggressor and leaves bid `Q`; CANCEL_OLDEST cancels the rest and leaves ask `Q-1`; CANCEL_BOTH leaves neither; DECREMENT_AND_CANCEL restates the rest to bid 1 and cancels the aggressor. The four prevention modes must print nothing. Actual assertion results are shown together in five comparison cards. |
| Iceberg loses priority | Rest sell iceberg total 6/display 2, then an ordinary sell 2, at `P`. The snapshot must expose 4. A buy of 5 must execute iceberg(2), peer(2), iceberg(1), proving replenishment joined behind the existing peer. Three public prints and the private fills must agree; visible residual is 1. |
| Cancel on disconnect | Log on a TCP user with COD enabled and rest two bid levels of `Q`. Abruptly close its TCP connection, observe cancel deltas totaling `2Q` and an empty snapshot. Reconnect the same owner and query each order: both retained reports must be ORDER_CANCELED with zero leaves. |
| FIX restart/recovery | Spawn the BFF's own bridge on an ephemeral loopback port with a private temporary **strict** FileStore and a fresh credential. FIX Logon 1 and order 2 yield outbound sequences 1 and 2. Kill/reap it, restart against the same store, and log on at inbound 3 without reset; outbound LogonAck must be 3. Resend sequence 2 must preserve ExecID and order fields, set PossDup=Y and OrigSendingTime to the original SendingTime. A TestRequest/Heartbeat barrier must use next outbound 4: replay did not consume sequence numbers. |

Gateway pool readers can deliver private reports in a different order from core
execution. Stop/iceberg assertions order the current core's monotonic `X<n>`
execution IDs numerically, never by network arrival or lexical string order.
The cascade independently verifies public print sequence and price; the iceberg
checks the expected fill quantities and visible residual. An unknown ExecID
format fails visibly rather than inventing an ordering.

Every completed assertion pauses 600 ms **for narration**, using a cancellable
timer. This is presentation pacing, not evidence of matching latency and not a
sleep used to determine correctness. A run has a 90-second deadline; individual
observations have bounded waits. Stop and timeout cancel sockets and owned
processes. Cleanup has its own deadline: it closes sessions, kills/reaps owned
bridges, removes their stores, reconnects run-scoped owners to mass-cancel any
remaining orders, and asserts an empty final book. Cleanup failure makes the
run fail. Empty core symbol tasks and retained core order-status history remain
until the stack is restarted; there is no symbol-deletion/admin API.

## API and streaming

| Route | Contract |
|---|---|
| `GET /api/scenarios` | `{scenarios:[{id,name,description}],run:...}`; most recent run or null |
| `POST /api/scenarios/{id}/run` | JSON `{ "seed": 71 }`; required safe signed integer; 202 returns `run_id`, `scenario`, `symbol`, `seed`, `state` |
| `GET /api/scenarios/{id}/events?run=<run_id>` | SSE with numeric event IDs; `Last-Event-ID` resumes without duplicating earlier events; history for the most recent run is retained in memory |
| `POST /api/scenarios/{id}/stop?run=<run_id>` | Cancel that exact run and await cleanup; a stale run ID cannot stop its replacement |

SSE events have `seq`, `kind`, and `elapsed_ms`. `step` events also carry `step`,
`panel`, `status` (running/passed/failed), `message` and deterministic `outcome`.
`complete` events carry passed/failed/stopped and the cleanup result. FIX recovery
also emits `evidence` with observed sequence, ExecID, PossDup and OrigSendingTime
fields (never authentication fields); these variable IDs/timestamps are excluded
from deterministic outcome comparisons. Allow-listed
`frame` events carry protobuf JSON and an actor label, feeding the existing
stores; int64 values stay decimal strings. Logon, JWTs, FIX credentials, raw
child output and signing material are never streamed. At 2,000 transcript events, further frames
cancel the run; the failure/cleanup terminal events are still retained.
SSE writes have deadlines and heartbeat comments. The browser reports stream
interruption and reconnects with replay; a decode failure is visible.

POST routes share the BFF's Host/Origin checks and bounded JSON decoding. The
server chooses every actor, symbol, port, executable and temporary directory;
the browser supplies only the known scenario id and seed. No Docker socket is
mounted or used. Scenario 6 talks to a subprocess, not the Compose bridge, and
cannot restart the user's normal FIX bridge.

## Demo configuration and validation

The demo overlay removes `FXP_SYMBOLS` from the core environment so unlisted
`SCN-*` books can be admitted, and sets `FXP_MAX_SYMBOLS=4096`. This is a lifetime
admission cap, not a count of nonempty books: restart the demo when exhausted.
Native runs must likewise **unset** `FXP_SYMBOLS` (an empty string is invalid)
and set a suitable positive `FXP_MAX_SYMBOLS`. Scenario symbols use default
scale/tick/lot rules. Admission rejection is surfaced as a failed setup.

`FXP_DEMO_FIX_BINARY` defaults to `bin/fix_bridge` natively and
`/usr/local/bin/fix_bridge` in the demo image. Build it with
`(cd fix_bridge && go build -o ../bin/fix_bridge .)`. It is trusted server
configuration; requests cannot choose a binary. `FXP_FIX_GATEWAY_ADDR` is a new
optional bridge setting, default `127.0.0.1:8082`; the owned child receives the
BFF's `FXP_SIM_GATEWAY_ADDR`. It receives only an allow-listed environment, the
shared JWT secret and fresh FIX credential via environment, and upstream TLS
settings mapped from the existing `FXP_CLIENT_*` variables. Parent core tokens
and unrelated secrets are excluded. Child output is discarded.

The only wire addition is v2.6 `SessionControl.symbol` (field 4). Empty retains
the existing all-symbol behavior used by simulator opening calls; a nonempty
symbol targets one book. Operator authorization is unchanged. No admin API,
hosted mode, latency panel or inspector change is part of this feature.

`web/e2e/scenarios.spec.ts` contains one Playwright test per scenario, each run
twice with seed 71 on fresh symbols and compared using asserted outcomes. These
run automatically in the existing `console-e2e` job. Go unit tests cover
single-flight execution, stale stops, seed repeatability, SSE replay, deliberately
failing assertions, panic cleanup and execution ordering. A tagged native
integration suite (`go test -tags scenariointegration -run TestRealScenariosRepeat
-v .` in `demo_api/`) repeats all six against a running core/gateway and the
configured bridge binary. A Rust regression proves scoped calls leave another
symbol continuous; existing all-symbol tests remain in place. Buf breaking must
remain clean against main.
