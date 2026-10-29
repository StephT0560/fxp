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

## Pending mass-cancel and status requests

The gateway shares one pending-request tracker for mass cancel and order status,
with a fixed **4096-entry aggregate cap** across all clients and both kinds.
A request rejected at the cap is not forwarded to the core. Both responses use
fresh internal correlation IDs; original client IDs are restored only on the
requester's response. Sender and response kind must match, so duplicate client
IDs, late replies, and foreign-owner replies cannot consume another request.
Cancel-on-disconnect requests have no client route and their aggregate reports
are dropped, as before; individual order cancellations keep their normal routes.

`FXP_GW_MASS_CANCEL_TIMEOUT_SEC` defaults to **30 seconds**. Values must be
integers from 1 through 86400; invalid values log a warning and fall back to 30.
Expiry removes the entry and sends `OrderMassCancelReport` with the original
request metadata and `reject_reason="mass cancel timeout"`. Status retains its
fixed 30-second deadline. Success, forwarding failure, disconnect, writer
failure and completed shutdown draining remove entries and stop their timers.
Registration racing a stopped/draining client is rejected. Timer callbacks retain
only the internal key/tracker, not the connection.

**A timeout is a missing confirmation, not a rollback.** The core may already
have canceled orders, or may finish later. Reconcile order state before retrying;
a late aggregate report is dropped rather than delivered to a newer request
that reused the same client ID. Individual execution reports remain independent.

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
connection pool. Terminal authentication responses are flushed before the
gateway closes a client connection. The bridge starts one shutdown deadline
before requesting Logout concurrently from logged-on sessions. A session that
does not complete Logout is closed when that deadline expires; the bridge
waits for its handler to finish before closing the session registry.

Core drains each connected client's critical frames before market data and
half-closes the TCP write side. This drain is independent of journal-held
senders, so resting-order owners do not keep writers alive past shutdown.
When an ExecutionReport has no execution ID, the FIX bridge supplies a
process-unique FIX ExecID (tag 17); existing core execution IDs are preserved
and core journal ID allocation is unchanged.

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
| `FXP_GW_MASS_CANCEL_TIMEOUT_SEC` | `30` | Mass-cancel confirmation deadline, 1–86400 seconds; shared pending cap is 4096; timeout does not undo core actions |
| `FXP_GW_MD_QUEUE` | `4096` | Per-client best-effort market-data queue |
| `FXP_SYMBOLS` | unset | Optional comma-separated symbol allow-list |
| `FXP_MAX_SYMBOLS` | unset | Optional positive limit on newly admitted symbols |

See the [README configuration tables](../README.md#gateway-authentication) and
Compose file for the full list of service settings.

## Subscriber isolation audit

The [baseline](performance/phase1-baseline.md) recommends the corrected
separate-process harness and no production optimization. Core output queues
are fixed at **8192 critical / 4096 market-data messages per connection**;
gateway defaults match and can be configured independently via the existing
`FXP_GW_CLIENT_QUEUE` / `FXP_GW_MD_QUEUE` settings. Enqueue is nonblocking at
both fan-out layers. Each connection has one socket writer; a blocked write
retains at most one additional in-flight frame, not another unbounded queue.
Critical work gets selection priority, but cannot interrupt a market-data
write already blocked on that same socket. Isolation applies to other
connections, not to ACKs on the stalled connection itself.

Overflow policy is drop-newest for market data. The core increments global
`MD_DROPPED` and per-connection `md_dropped`; the gateway increments
`MarketDataDropped()` and per-writer `mdDropped`. The core counters are internal; per-connection depths/drop
counts are currently inspected by tests, not exposed as a new metrics endpoint.
Neither counter is a durable per-symbol loss ledger. Capacity bounds queued
**message count**, not total process RSS: retained bytes depend on frame sizes,
and books, symbol/connection counts, kernel socket buffers, TLS/WS buffers and
allocator overhead are separate. The functional fixed-payload check measures
retained heap after GC in addition to asserting exact queue depths.

Critical overflow is a visible connection failure. Go closes the socket and
returns `errClientQueueFull`; Rust signals its handler, which aborts the writer
and closes the connection. Already queued reports may be lost on disconnect;
there is no successful-session silent-drop contract. Gateway handler cleanup
sends owner-scoped `MASS_CANCEL_REASON_DISCONNECT` when the authenticated
Logon's `cancel_on_disconnect` is enabled; the default is disabled. This is
best effort if the core connection is unavailable, not a durable cancellation
acknowledgement. Direct native core clients do not receive gateway Logon/COD
semantics. Reconcile orders after reconnecting, including when COD was enabled.

Trade-sequence gaps detect dropped prints only. Level-only loss is not
sequence-detectable today; see [market-data delivery limits](market-data.md#subscriber-backpressure-and-remaining-limits).
No queue, wire format, FIX behavior, timer or production fan-out policy was
changed by this audit.
