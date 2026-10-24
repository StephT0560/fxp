# Operations

## Slow consumers and output queues

Core connections use bounded output queues. Execution reports and other
critical responses are not silently dropped: a client that fills its critical
queue is disconnected. Market-data updates may be dropped when a subscriber's
queue is full. The gateway applies the same policy independently per client,
so a stalled reader cannot block fan-out to healthy clients. Gateway queue
sizes are controlled by `FXP_GW_CLIENT_QUEUE` (default `8192`) and
`FXP_GW_MD_QUEUE` (default `4096`). The gateway's `MarketDataDropped()` metric
counts discarded market-data messages.

Clients should reconnect after a disconnect and use FIX sequence numbers and
ResendRequest where applicable to recover critical FIX session output. A
market-data consumer should treat updates as best-effort and resubscribe or
request a fresh snapshot after reconnecting.

## Symbol admission

`FXP_SYMBOLS` is an optional comma-separated allow-list. Values are trimmed;
an empty allow-list entry does not admit an empty symbol. When unset, any
non-empty symbol may be admitted. `FXP_MAX_SYMBOLS` optionally limits the
number of distinct symbols created at runtime; it must be a positive integer.
Unset means unlimited. Invalid startup configuration is logged and exits
non-zero.

The admission check is applied only when a symbol would be created. Commands
for an unknown symbol, including cancel, replace, mass-cancel, and market-data
subscription requests, do not create symbol tasks. New orders that cannot be
admitted receive a rejection without consuming a journal execution ID.

When core event-log replay restores previously persisted symbols, it restores
them even if the current allow-list or symbol limit would reject new admission.
Core logs a warning for each such restored symbol. This preserves existing
orders and their replay state while keeping the configured limits effective
for new symbols.

## Graceful shutdown

Core, gateway, and FIX bridge handle SIGINT and SIGTERM. The default shutdown
deadline is five seconds and can be changed with `FXP_SHUTDOWN_TIMEOUT_SEC`,
which must be a positive integer. Core stops accepting connections, asks
handlers to stop reading, drains symbol tasks and output writers, and syncs
the event log. If its deadline expires, core logs unfinished work and exits
non-zero.

The gateway stops accepting TCP and WebSocket clients, interrupts client
reads, drains queued writers within the deadline, and closes its core
connection pool. The bridge stops accepting FIX clients, sends Logout to
logged-on sessions, waits for sessions to end, then closes the session
registry. A session that does not complete Logout is closed at the shutdown
deadline so its store can be flushed and closed.

Core rejects a non-positive or non-integer timeout and exits during startup.
The Go services log invalid timeout values and use the five-second default.

The Compose services use `init: true` for signal forwarding and a 15-second
`stop_grace_period`, which leaves time for the processes to finish their
application-level shutdown after Docker sends SIGTERM.

## Useful configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `FXP_SHUTDOWN_TIMEOUT_SEC` | `5` | Graceful-shutdown deadline for core, gateway, and bridge |
| `FXP_GW_CLIENT_QUEUE` | `8192` | Per-client critical output queue |
| `FXP_GW_MD_QUEUE` | `4096` | Per-client best-effort market-data queue |
| `FXP_SYMBOLS` | unset | Optional comma-separated symbol allow-list |
| `FXP_MAX_SYMBOLS` | unset | Optional positive limit on newly admitted symbols |

See the [README configuration tables](../README.md#gateway-authentication) and
Compose file for the full list of service settings.
