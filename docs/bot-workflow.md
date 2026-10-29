# Reproduce a bot failure

This local example quotes both sides, receives a seeded partial fill, sends a
bad replace, records the run, then verifies and explains the rejected request.
The target is a first diagnosis in under ten minutes on a development machine
with Rust, Go and Python installed. The first release build and dependency
downloads dominate setup time; cached runs take seconds. No Docker, console,
hosted service or PyPI publication is involved.

## From a clean checkout

Use Linux, macOS or WSL with Bash, Make, OpenSSL, Git, Python **3.10+** with venv,
Go (version in `go_gateway/go.mod`), Rust stable with Cargo, and **protoc 25.3** on
PATH. If needed, install the matching prebuilt archive from the
[protobuf v25.3 release](https://github.com/protocolbuffers/protobuf/releases/tag/v25.3).
Keep its `include/` directory beside `bin/` for the standard timestamp schema.
The build uses the repository's locked Rust dependencies. Initial setup needs
package-registry access; the running demo only connects over loopback.

```sh
git clone https://github.com/StephT0560/fxp.git
cd fxp
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -e sdk/python
make repro-demo
```

Stop an existing stack first (`make demo-down` if using the console demo).
Ports 8080, 8081 and 8082 must be free. The script refuses occupied ports or an
existing requested output directory. It starts only its own core and gateway,
uses disposable JWT credentials and plaintext **loopback** listeners, and stops
both on exit. Do not expose this development configuration to a network.
Inherited `FXP_*` deployment settings are cleared in child processes so a session
schedule, TLS setting or old journal cannot silently change the example.

The script generates Go protobuf bindings, builds the release core, gateway and
`fxp-replay`, and records a fresh `fxp-run-v1` bundle. It waits for real order
acknowledgments and fills, rather than sleeps, to order the scenario. After the
bot finishes, it stops the gateway and cleanly stops the core to finalize the
bundle. Then it runs `verify`, `timeline`, and `explain`, and cross-checks a short
diagnosis against the verified recorded events.

Expected conclusion (the execution IDs, times, sequences and hashes vary):

```text
IDENTICAL events=... symbols=... global=... state=...
...
demo-maker::quote-7-ask: ...
BOTTEST demo-maker::quote-7-ask owner=demo-maker: replace rejected: Replace quantity not above filled quantity ...
...
BUG REPRODUCED: demo-maker::quote-7-ask filled 7/10, leaving 3.
The replace command sent new_quantity=3 as TOTAL quantity, which does not exceed
 the 7 already filled. Engine reason: Replace quantity not above filled quantity.
```

A successful demonstration exits **0** because reproducing the deliberate defect
is expected. An unexpected response, failed assertion or replay mismatch fails
the command. The bot is intentionally broken; do not use it for real trading.

## What failed

[`bot.py`](../examples/bot_repro/bot.py) quotes ten units at each side of a spread.
It tracks remaining quantity but mistakenly uses it as the total quantity on a
replace. Seed **7** makes the taker buy seven units of its ask first. The bot then
sends `new_quantity=3`. The engine already filled seven units and rejects a new
total that does not exceed the filled quantity. The original three-unit quote
remains live until the example's explicit mass-cancel cleanup.

This is one quantity-semantics bug: replacement `new_quantity` means **total
quantity**, including previous fills. To preserve three remaining after seven
fills, send total **10**, or **0** to keep the quantity. Real bot code must also
track replacement IDs, reconcile reports and recover state after disconnects.

The harness owns a `random.Random(seed)` instance. Seed 7 selects a seven-unit
partial fill; seed **1** selects no fill before repricing, where leaves and total
are both ten and the defect stays hidden. The bot itself is not randomized.

```sh
FXP_REPRO_SEED=1 make repro-demo
# CONTROL: no partial fill before replace; leaves 10 equals total 10.
```

The trace proves the invalid quantity and records the engine's rejection reason.
The source code identifies the bot bug; neither `explain` nor replay invents the
bot's motivation. The original quote's fills and replace rejection are shown
under the same order. The gateway namespaces IDs as `<owner>::<client ID>` in
core recordings and strips that prefix on client responses. Use the full core
ID in the replay CLI.

## Inspect or share the result

Every invocation prints its directory, such as `target/bot-repro.ABC123/`:

| File | Purpose |
| --- | --- |
| `bundle/` | Manifest, unchanged command log, recorded causal trace and expected digests/state hash |
| `scenario.json` | Seed, client/core IDs and observed failure; helper output, outside the bundle guarantee |
| `verify.txt` | Fresh-engine offline verification result |
| `timeline.txt`, `timeline.json` | The failing order's commands, fills, rejection and cleanup |
| `explain.txt`, `diagnosis.txt` | Event-derived explanation and checked example diagnosis |
| `build.log`, `core.log`, `gateway.log` | Local troubleshooting logs |

```sh
# Substitute the directory printed by your run.
BUNDLE=target/bot-repro.ABC123/bundle
REPLAY=fxp_core/target/release/fxp-replay
"$REPLAY" verify "$BUNDLE"
"$REPLAY" timeline "$BUNDLE" --order demo-maker::quote-7-ask --owner demo-maker --symbol BOTTEST
"$REPLAY" explain "$BUNDLE" --order demo-maker::quote-7-ask --owner demo-maker --symbol BOTTEST
"$REPLAY" export "$BUNDLE" --jsonl > events.jsonl
```

Share the **whole bundle directory** for verification. It can contain account,
owner and order identifiers, submitted commands and all owners' business events.
Review that data before sharing. CLI owner filtering and counterparty redaction
are display choices, not access control over bundle files. Logon JWTs are not
engine commands and are not recorded in `commands.log`.

The [reproducibility contract and format](reproducibility.md) apply:
replaying the recorded commands produces identical ordered business events and
final market state. It does **not** rerun or reproduce a bot's decisions, clocks,
randomness, scheduling, network delays, gateway routing or transport metadata.
Two seeded live runs need not have the same log or hashes: real clocks, generated
transport IDs and arrival timing still vary. Each saved run must verify against
its own expected results. A crash leaves an incomplete bundle; `verify` exits 2
instead of claiming success. Recording retains per-command event-log fsync speed;
there is no group commit.

## Attach a real bot

The local SDK lives in `sdk/python/fxp_client` and uses only the `protobuf` runtime.
This small version chooses TCP (four-byte **big-endian** payload length followed
by an `FXPMessage`); it does not add a WebSocket dependency. Integer prices are
minor units from the venue's symbol configuration, not floating-point prices.

```python
from fxp_client import Client, dev_token, pb

# Local development only: FXP_JWT_SECRET must match your local gateway.
# In real deployments obtain a JWT from your issuer, not from this helper.
token = dev_token("my-bot")
with Client.connect("127.0.0.1", 8082, token, timeout=10) as client:
    client.new("q1", "BOTTEST", pb.SELL, 10, 10100,
               client_order_id="strategy-quote-1")
    # A symbol must exist in the core before its market-data subscription.
    # Wait for EXEC_NEW, then subscribe; use one event consumer.
    for event in client.events():
        if event.trade_gap:
            # Pause decisions and reconcile; no automatic gap recovery is provided.
            print("Trade sequence discontinuity:", event.trade_gap)
        msg = event.message
        if event.kind == "execution_report":
            report = msg.execution_report
            if report.order_id == "q1" and report.execution_type == pb.EXEC_NEW:
                client.subscribe("BOTTEST")
            print(report)
```

`new`, `cancel`, `replace`, `mass_cancel` and `subscribe` send requests; the event
stream carries their responses. Handle `execution_report`, `error_message` and
mass-cancel rejection reasons explicitly; sending is not acceptance. `replace`
accepts generated optional fields, including `new_display_quantity=None` (keep)
versus `0` (fully display). Authentication failure raises `PermissionError`.
Protocol/framing errors, timeouts and socket errors close the connection; clean
EOF ends `events()`. The configured timeout applies to socket operations, not an
entire multi-message transaction. There is one reader, no background task, no
thread safety, automatic reconnect or automatic resend. Reconcile accepted
orders before retrying after a disconnect; a blind resend can duplicate intent.
The low-level `send` method accepts any generated envelope for advanced use.

The first observed trade establishes a baseline separately for each symbol.
Thereafter gaps, duplicate sequence numbers and regressions are attached as
`Event.trade_gap` without suppressing the message. A high-water mark prevents a
regression from hiding the next gap. This detects observed stream discontinuity;
it cannot prove no trades were missed before subscribing or across core restarts.

For a TLS gateway use the stdlib context rather than disabling verification:

```python
import ssl
context = ssl.create_default_context(cafile="certs/ca.crt")
# For mTLS: context.load_cert_chain("certs/client.crt", "certs/client.key")
client = Client.connect("127.0.0.1", 8082, token,
                        tls=context, server_hostname="localhost")
# Consume events using a context manager or close it explicitly.
client.close()
```

For your own stack, set `FXP_RUN_RECORD` to a **new** directory on the core, and
optionally set `FXP_RUN_META_JSON` to JSON seeds/configuration identifiers. Use
normal gateway authentication and symbol configuration. Start your bot, stop it,
then stop the gateway and core cleanly before verifying. The demo launcher
intentionally clears inherited deployment settings; it is not a launcher for
production bot configuration. No SDK code needs a bundle path or direct core access.

## Configuration, generation and tests

| Variable | Default | Meaning |
| --- | --- | --- |
| `FXP_REPRO_PYTHON` | `python3` | Interpreter with the local SDK installed (an activated venv works) |
| `FXP_REPRO_SEED` | `7` | Seeded stimulus (`1` is the no-fill control) |
| `FXP_REPRO_DIR` | New `target/bot-repro.XXXXXX` directory | Explicit **new** output directory; parent must exist |
| `FXP_REPRO_TEST_CLIENT` | `0` | `1` also runs the SDK local-stack integration assertions before the bot |
| `FXP_REPRO_TCP_PORT` | `8082` | Endpoint for directly invoked scenario/integration scripts only; the launcher clears this override and owns ports 8080–8082 |

The SDK's local signer reads `FXP_JWT_SECRET`, `FXP_JWT_ISSUER` (default
`fxp-dev`), `FXP_JWT_AUDIENCE` (`fxp-gateway`) and optional `FXP_JWT_KID`.
It requires a secret of at least 32 bytes, a subject, and TTL 1..86400 seconds
(default 900), uses fresh token IDs, and never prints the token. It is a local
helper, not an identity provider.

Bindings are checked in; generating them needs protoc **25.3**, but installing or
using the SDK does not. Regenerate from the single authoritative schema:

```sh
bash scripts/gen_python_proto.sh
bash scripts/gen_proto.sh
(cd go_gateway && go run ./cmd/python_golden ../sdk/python/tests/golden)
git diff --exit-code -- sdk/python/fxp_client/fxp_pb2.py sdk/python/tests/golden
python -m unittest discover -s sdk/python/tests -v
python -m unittest discover -s examples/bot_repro -v
FXP_REPRO_TEST_CLIENT=1 make repro-demo
```

The Go encoder independently produces length-prefixed command/response vectors,
large signed/unsigned integers, optional-presence cases and an HS256 JWT vector.
Python compares exact bytes and decoded values; it does not generate its own
expected frames. Integration covers real Logon acceptance/rejection, subscribe,
public trade prints, partial fills, replace, cancel and mass cancel. CI's required
`repro-smoke` job runs on Python 3.10, checks generation drift, exercises that
integration plus the seed-7 rejection and seed-1 control, and uploads the bundles.
`ci-success` requires this job. The bot unit tests explicitly preserve its single
deliberate mistake so the demonstration cannot silently stop reproducing it.
