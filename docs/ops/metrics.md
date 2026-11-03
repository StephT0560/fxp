# Core and gateway metrics

Metrics are off by default. An unset or empty `FXP_CORE_METRICS_ADDR` (core) or
`FXP_GW_METRICS_ADDR` (gateway) opens no metrics listener and does not collect
counters. Set a port (`9100`) or `:9100` to bind **127.0.0.1**. Set a full IP
address/port to choose another interface, including `[::1]:9100`. A public bind
such as `0.0.0.0:9100` is explicit opt-in. Neither endpoint shares a trading
listener or implements authentication; restrict it to a private monitoring network.

Build with `make build` after installing the normal prerequisites. For example,
in separate terminals with the same fresh `FXP_JWT_SECRET` for gateway/clients:

```bash
FXP_CORE_METRICS_ADDR=9100 cargo run --release --locked --manifest-path fxp_core/Cargo.toml
(cd go_gateway && FXP_GW_METRICS_ADDR=9101 go run .)
curl --fail http://127.0.0.1:9100/metrics
curl --fail http://127.0.0.1:9101/metrics
```

Only `GET /metrics` is served, in Prometheus text exposition format 0.0.4.
Configuration and bind failures fail process startup. Metrics readiness is **not**
trading readiness: scrape serving may start before journal recovery/pool setup.
Connections have two-second header/write deadlines and bounded headers; the core
admits at most 32 concurrent scrape handlers. Counters are relaxed atomics,
scrapes are approximate snapshots during concurrent updates, and no disk writes
are added by scraping. All counters reset at process restart. No matching rules,
command ordering, journal format, or fsync policy change.

## Metric inventory

| Name | Type | Process | Labels | Meaning |
| --- | --- | --- | --- | --- |
| `fxp_connected_clients` | gauge | both | none | Active transport connections. Gateway counts TCP handlers/upgraded WS clients, including pre-Logon clients. Core counts accepted connections including core-auth handshake; these are usually **gateway pool sockets**, not individual bot users. |
| `fxp_messages_total` | counter | both | `direction`, `type` | Client-facing wire frames read (`in`) or successfully written (`out`). Core includes every complete input frame; gateway includes decoded input messages admitted past its rate check. Outbound counts include each recipient, exclude queued/dropped/failed writes. Type comes from the first FXP wire tag; unknown types use `unknown`. |
| `fxp_rejects_total` | counter | both | `reason` | Rejection responses generated. Core observes before queueing; gateway observes locally generated rejections and proxied execution-report rejections. Includes failed Logon, protocol errors and rejected status/control/mass-cancel responses. Delivery is not required; STP cancellations and historical rejects inside status snapshots do not count again. |
| `fxp_commands_processed_total` | counter | both | none | Core: completed symbol-task operations, including timer ticks, subscriptions and queries; not output/event count. Gateway: supported commands dispatched by the authenticated router, including subsequent validation/core-availability rejections. These scopes are different; do not sum them as engine executions. |
| `fxp_commands_forwarded_total` | counter | gateway | none | Supported command frames fully written to core pool sockets, including internal/replayed subscriptions and COD requests. A successful write is not an execution confirmation; reconnect retries can add writes. |
| `fxp_journal_fsync_total` | counter | core | none | Journal `sync_data` attempts for appended commands. Excludes header/directory syncs and final bundle metadata/trace syncs. Zero without recording/event-log persistence. |
| `fxp_journal_fsync_errors_total` | counter | core | none | Failed journal `sync_data` attempts. Existing persistence failure behavior is unchanged. |
| `fxp_journal_fsync_duration_seconds` | histogram | core | `le` on buckets | Time spent in those `sync_data` calls, including failed calls; excludes journal serialization/write. Emits `_bucket`, `_count`, `_sum` in seconds. |
| `fxp_process_uptime_seconds` | gauge | both | none | Monotonic elapsed seconds since metrics/process initialization, independent of wall-clock changes. |
| `fxp_build_info` | gauge, always 1 | both | `version`, `git_sha` | Core uses Cargo version/build provenance. Gateway uses version 0.1.0 and Go VCS build info; release images inject their version/SHA with linker flags. Source archives without provenance report `unknown`. |

Histogram bounds in seconds: `0.0001`, `0.00025`, `0.0005`, `0.001`, `0.0025`,
`0.005`, `0.01`, `0.025`, `0.05`, `0.1`, `0.25`, `1`, `+Inf`. Bucket values
are cumulative; `+Inf` equals `_count`. Separate metric snapshots can differ
slightly during an in-flight fsync.

`direction` is only `in` or `out`. `type` is only:

```text
unknown heartbeat logon logout gap_fill new_order order_acknowledgment
order_cancel execution_report market_data_snapshot market_data_incremental
market_data_request trade_capture_report error_message logon_ack
order_cancel_replace trading_session_status auction_info order_mass_cancel
order_mass_cancel_report order_status_request order_status_response
session_control session_control_report
```

`reason` is only `authentication`, `permission`, `rate_limit`, `capacity`,
`unknown_order`, `duplicate`, `invalid`, or `other`. They are broad, fixed
categories derived from response reasons, not a lossless business-reject taxonomy.
No owner, session, token, symbol, order ID or raw error text is a label. Build
labels are trusted build metadata, fixed for a process's lifetime.

## Containers

Metrics remain off in existing Compose stacks. To scrape a container externally,
use a Compose override that sets the relevant variable to an explicit container
interface, for example `0.0.0.0:9100`, and publishes only `127.0.0.1:9100:9100`.
For container-to-container scrapes, put the scraper on the private network and
avoid publishing the port. Do not reuse a public trading/edge listener.

## Validation and performance

Tests use the maintained **test-only** parser in `tests/ops/requirements.txt`:

```bash
(cd fxp_core && cargo build --release --locked)
(cd go_gateway && go build -o ../bin/go_gateway .)
python3 -m pip install -r tests/ops/requirements.txt
python3 -m unittest discover -s tests/ops -v
```

Keep ports 8080–8082 free. Endpoint tests run a fresh recorded core/gateway, parse
both scrapes, submit a real command/rejection, check privacy and histogram
consistency, SIGTERM the stack and verify its recording remains `IDENTICAL`.
Gateway unit/race tests also cover opt-in defaults and address validation.

The before/after measurements and their limits are recorded in
[metrics benchmark](metrics-benchmark.md), using the existing
[benchmark methodology](../benchmark-methodology.md). Metrics-disabled and
metrics-enabled builds must be compared separately. Do not infer a capacity/SLO
from these shared-host loopback measurements.
