# Core input-event persistence (#8)

## Contract and rollout

`FXP_EVENT_LOG=<path>` opts the core into single-node input replay. Unset means
fully disabled: retain the existing per-symbol tasks, wall clock and global
ExecID allocator, with no file opens, replay or fsync. An explicitly empty path
is an error. Enable first on a disposable test environment, then a stopped
single-node deployment using a new private file on durable local storage.
The parent directory must already exist and be writable. Never share a log
between nodes. The core holds an exclusive OS file lock for its lifetime.

Stop the core before backup, restore, moving or replacing its log. Preserve the
session schedule and binary version with the backup. Disabling the flag starts
an empty in-memory exchange; re-enabling an old file resurrects its history, not
the intervening unlogged state. This is not a migration of an already-running
in-memory book. Restrict file/directory access: order IDs, traders, accounts and
order details are sensitive. Compose passes through the flag and provides an
optional core data volume; use `FXP_EVENT_LOG=/data/core_store/events.log`.

## Ordering and deterministic inputs

Enabled mode uses one synchronous coordinator across all symbols. Under its
lock it samples the live clock once, appends and fsyncs the input, sets the
engine's injected clock to that exact time, and applies the input before
releasing the lock. Socket writes happen afterward, outside the coordinator.
This trades latency and cross-symbol parallelism for a well-defined total
order. Synchronous disk I/O can block a runtime worker; this first implementation
does not claim production throughput.

Each engine input is logged, including rejected requests: new order, cancel,
cancel/replace, timer tick, and subscription (which itself advances expiry).
The record contains the target symbol, the injected time (signed Unix seconds
and nanoseconds), a global monotonically increasing sequence starting at 1, and
the input payload. Cancel and replace payloads contain every field the engine
consumes; connection routing handles and subscriber lists are not persisted.
A symbol's first record creates its engine at that record's time, both live
and during replay. Periodic ticks continue for recovered symbols even when
there are no clients.

The shared ExecID counter starts at 1 and increments for every generated report,
including rejections and reports discarded during replay or downtime. Replaying
all inputs in their global order reproduces the next counter value; no separate
counter checkpoint is required. There must be no unlogged path into enabled
engines. Startup catch-up ticks are ordinary durable inputs too.

## Record format and framing

The file begins with eight-byte magic/version `FXPEVL01`, followed by a framed
configuration record (sequence 0). Its payload identifies the engine replay
semantics version and exact parsed session schedule. A mismatch fails startup;
do not reinterpret an existing log under different schedules or matching rules.
Any future change that can affect deterministic replay needs a version bump or
an explicit migration, even if the network protocol is unchanged.

Each frame is:

| Bytes | Meaning |
|---|---|
| 4 | unsigned big-endian protobuf body length, 1 through 1 MiB |
| length | private protobuf record body |
| 4 | big-endian IEEE CRC-32 of the length prefix plus body |

The body has sequence (u64), seconds (i64), nanos (i32), symbol (string), input
kind (u32), and payload (bytes). Kinds: 0 configuration, 1 new, 2 cancel,
3 replace, 4 tick, 5 subscribe. Request payloads use the existing FXP protobuf
messages; tick/subscribe payloads are empty. This format is internal to the
core and does not change the network protobuf schema. Lengths are bounded
before allocation. CRC is accidental-corruption detection, not authentication.

## Durability and failures

Every input frame is written in full and `sync_data` succeeds **before** engine
application or acknowledgement. There is no relaxed/group-commit option yet.
New log creation fsyncs both the file and its parent directory. Tail truncation
is fsynced before appending again. Opening/locking/replay errors abort startup;
append or fsync errors fail the process closed, not just one symbol task.
An input durably appended immediately before a crash may execute on recovery
even though the client never saw an acknowledgement. Clients must reconcile
ambiguous outcomes; this is not exactly-once client delivery.

Scan frames from the beginning on startup. A torn length/body/checksum, invalid
length, checksum mismatch or undecodable record ends the valid prefix. Truncate
at the last good offset, fsync, and emit a warning with the discarded byte count
and offset. Do not scan forward looking for another frame: if corruption is in
the middle, the whole suffix is lost and needs operator reconciliation.
An invalid file header/configuration, unsupported input kind, malformed semantic
payload, or sequence gap fails startup instead of silently deleting history.
Never automatically truncate on an ordinary disk read error.

## Replay and downtime

Before binding the listening socket, validate the header and replay each record
through the same engine entry methods with the clock fixed to its recorded
time. Owners use a disconnected sink and outputs are discarded: clients and
subscribers do not survive a restart. Replaying the same inputs works because
the engine is synchronous and deterministic, with clock and ExecIDs injected;
there is no I/O or randomness in its state transitions.

After replay, sample the live clock and durably apply a first tick to each
recovered symbol (sorted by symbol for a stable startup order). This advances
every crossed boundary from the last recorded time: Day/GTD expiry and opening
and closing auctions happen with no audience. Resume normal one-second ticks
and live input timestamps afterward. Backward wall-clock moves retain the
engine's existing high-water boundary behavior.

## Scope and limits

- **All live orders are restored**, not just GTC: Day, GTD, parked stops,
  partial-fill report state, held AT_CLOSE orders, call markets and auction phase
  follow from replay. Expired orders disappear on catch-up.
- The gateway's `orderClientMap` is **not restored**. Post-restart reports for
  restored orders go nowhere until gateway routing/reconnection work happens.
  A snapshot or a correctly owned cancel sent directly to core can reconcile
  restored state; do not promise FIX/gateway report recovery.
- No snapshots, compaction, retention or index yet. Startup scans the full log
  and holds decoded records in memory during replay; size and replay latency grow
  without bound. Timer ticks themselves grow the log.
- Single-node/local-file only: no replication, consensus, failover or HA claim.
- CRC detects accidental corruption only; it is not protection from malicious
  modification or storage loss. Keep verified backups.
- File locks and directory fsync must work on the chosen filesystem. Avoid
  network filesystems. Operationally validate the durable path and space before
  rollout.

## Verification

Unit tests cover framing round-trip, checksum known vector, torn/corrupt suffix
repair, locking, incompatible configuration and fail-closed append behavior.
A seeded mixed-input history (multiple symbols, cancels, replaces, rejects,
subscriptions, ticks, stops and auctions) compares live versus reopened engines,
including book, stop state, phase, report state and next ExecID. Restart tests
check GTC/GTD survival and Day expiry across downtime. The E2E harness performs
a real core restart and checks a restored GTC snapshot and owned cancellation
directly against core, avoiding the known gateway routing limitation.
