# FIX conformance

## Session schedule and reset

Scheduling is disabled by default. Configure both `FXP_FIX_START_TIME` and
`FXP_FIX_END_TIME` (`HH:MM:SS`). Add both `FXP_FIX_START_DAY` and
`FXP_FIX_END_DAY` (full weekday names) for a continuous weekly window.
`FXP_FIX_TIME_ZONE` is an IANA zone, default UTC. Daily windows may cross
midnight. Start is inclusive, end exclusive. Equal endpoints represent a full
day/week, still resetting at the endpoint. Calendar arithmetic follows Go's
IANA timezone rules (including its resolution of ambiguous/nonexistent local
instants). Prefer boundaries outside DST transition hours.

At the end boundary, the acceptor sends Logout and closes the connection. It
archives and resets the durable store before releasing that session identity.
The boundary monitor checks every 100 ms; inbound reconnect also checks the
persisted boundary. A Logon outside the window gets an explicit Logout. A
session absent across an end boundary resets before accepting its next Logon.
Both inbound and outbound sequence numbers restart at 1; clients must start at
1 in a new window. Files under `archive/<boundary-unix-seconds>/` preserve the
previous outbound log and sequence numbers. A synced reset intent is recovered
on startup if reset was interrupted. Archives need operator retention policy.
Enabling scheduling on an existing store starts a new scheduled epoch and
archives that store on first authenticated Logon.

`FXP_FIX_RESET_ON_LOGON`, `FXP_FIX_RESET_ON_LOGOUT`, and
`FXP_FIX_RESET_ON_DISCONNECT` are independent booleans, default false.
Logon reset requires sequence 1. Logout/disconnect resets run after disconnect
callbacks and before the registry releases the session. Ordinary unscheduled
reconnects retain durable state, as before. Protocol tag 141=Y remains supported.
No reset policy enables initiator mode or TLS.

## Dictionary validation

The acceptor embeds the full QuickFIX v1.15.1 FIX44 dictionary and a separate
MIT venue profile. This product includes software developed by
quickfixengine.org (http://www.quickfixengine.org/). See the preserved
[license and provenance](../fix_bridge/codec/spec/README.md).

Required fields in present optional components are checked too. Each group has
its own duplicate/order/required-field scope. Missing tags use reason 1,
undefined tags 0, tags from a different message type 2, empty values 4,
invalid enums 5, incorrect datatypes 6, duplicate tags 13, misplaced header/trailer fields 14, group order 15 and
incorrect NumInGroup counts 16. Replies include RefTagID/RefMsgType and consume
the invalid message's inbound sequence number. Garbled framing/checksum is
still ignored without consuming a sequence number. Unknown application types
are answered with BusinessMessageReject by the application handler.

`FXP_FIX_DICTIONARY_STRICT=false` permits unknown user-defined tags >=5000.
Strict mode rejects them; declared STP and COD extensions remain allowed.
Standard tags are checked regardless of strictness. The XML venue profile
requires Symbol (the venue has no alternate instrument identifier resolver).
The previous tests' placeholder TransactTime=1 and their expectation that a
bad Side enum reached order business validation were technically invalid;
fixtures now use proper timestamps and expect dictionary Reject reason 5.

## Order status

FIX H resolves ClOrdID(11), falling back to OrderID(37), within the authenticated
owner. Symbol(55) and Side(54) are checked through the dictionary; 790 identifies
the inquiry (falls back to the queried ID if omitted). FIX AF supports
MassStatusReqType(585)=1 for Symbol or 7 for all retained owner orders, optionally
filtered by Side. Other mass-status types receive a business rejection.

The additive FXP v2.3 request/response uses new envelope fields 20/21 and
ExecutionType 10 (`EXEC_ORDER_STATUS`). Existing wire numbers are unchanged.
Gateway authentication overwrites sender and namespaces the query ID. Reply
routing uses fresh internal request IDs; it never changes live order routes.
The response is batched by encoded size (at most 1 MiB) and at most 64 reports and supplies completion
and total fields. Inquiries time out after 30 seconds. Disconnect removes routes.

Each symbol snapshots its serialized engine state after applying due expiry.
Mass queries across symbols are independently consistent per symbol, not a
global atomic snapshot. Reports include new, partial, filled, canceled, expired
and parked-stop states, with cumulative/leaves/average values. They use FIX
ExecType I and ExecID 0, and do not create fills. Mass replies echo 584, set
911=total reports, and set 912=Y on the final report. Empty mass results and
unknown/foreign single-order IDs get BusinessMessageReject 380=1; the latter
share exactly the same text and disclose no existence information.

Live state remains queryable; at most 100,000 terminal transitions per symbol
are retained in memory. Older terminal orders become unknown. Input-log replay
reconstructs status history with the same bound when FXP_EVENT_LOG is enabled;
without that log, core restart loses status history. FIX FileStore durability
alone persists FIX sequence/resend state, not matching-engine history.

Acceptance probes also cover empty/missing CompIDs (dictionary reasons 4/1,
before identity mismatch), optional header placement, and FIX decimal lexical
syntax (no leading plus, exponent, NaN or infinity). Session test fixtures now
place PossDupFlag/OrigSendingTime in StandardHeader, before body fields.

The first three fields must be BeginString, BodyLength and MsgType; malformed
header prefixes are ignored without consuming sequencing state. SequenceReset
in Reset mode ignores the MsgSeqNum value (including 0) but still validates
its dictionary fields; GapFill and ordinary messages require positive sequence
numbers. Rejects retain RefMsgType for the affected administrative message.

Permitting undeclared user-defined tags does not permit empty values or duplicate
fields: the validator still returns reasons 4 and 13 in the current scope.

Reusing a terminal order ID at the trusted core boundary binds its new status
record to the newly accepted owner. A rejected duplicate cannot change that
ownership. Gateway namespacing continues to prevent cross-owner ID collisions.

## QuickFIX acceptance baseline

The CI matrix runs all 59 definitions in each policy, using an explicitly
opted-in, compile-gated loopback echo fixture. Results: **45 strict passes / 14
allow-listed / 0 unexpected** under default policy, and **57 / 2 / 0** under the
QuickFIX preset. The two common exclusions are `14a_BadField.def` (legacy invalid
tag syntax/default user-tag policy) and `21` (SecurityDefinition). Default policy
adds seven Logon-silence differences, four fatal-close differences and one
heartbeat-expiry Logout difference. Exact grouped names and the qualifications
for TCP-close timing are in [the runner guide](../tests/quickfix/README.md).
No remaining allow-list entry is blocked on a missing-quantity echo fixture.

The echo fixture is absent from production builds and requires build tag
`quickfix_fixture`, `-test-fixture`, `FXP_FIX_TEST_FIXTURE=quickfix`, actual and
literal loopback, and no TLS/core token. It warns at startup, inserts quantity 1
only for absent OrderQty, and preserves existing invalid fields for validation.
It echoes validated orders without execution; it does not certify venue
application behavior. Normal missing/invalid quantity rejection remains intact.
The fixture uses fixed TestReqID TEST and fresh ephemeral sequences between
connections, consistent with the upstream test application's assumptions.

Production session fixes reverse OnBehalfOf/DeliverTo CompID, SubID and
LocationID, plus sender/target SubID and LocationID, on immediate session replies;
primary authenticated session CompIDs are preserved. Empty routes are omitted
and each reply uses its own triggering request (no route leakage to later or
asynchronous replies). Replayed application frames retain their stored routing.
High-sequence admin/application messages are buffered until sequence recovery,
then dispatched once through normal validation. Limits are 64 messages, 256 KiB
of field storage accounting, and a sequence distance of 4096; overflow closes the
session. GapFill discards covered messages and explicit reset clears the buffer.
Repeated low-sequence ResendRequests are validated and served again without
advancing input or output sequence state or executing application requests.
PossDup timestamp rejects retain RefMsgType/RefTagID; an accuracy error is fatal.

Heartbeat monitoring starts after Logon with the negotiated interval, avoiding a
sleep based on the pre-Logon default. Pending TestRequests suppress additional
idle heartbeats; the matching ID is still required. Production IDs remain unique.

## Session policy configuration

Defaults remain explanatory Logout for Logon failure and immediate close after
fatal Reject/Logout. The default heartbeat expiry still sends diagnostic Logout.
No preset changes authentication, TLS requirements or dictionary validation.

- `FXP_FIX_COMPAT=default` (or unset): current policies.
- `FXP_FIX_COMPAT=quickfix`: silent Logon failures, bounded waiting for the peer's
  Logout after fatal errors, and silent close for an unanswered TestRequest
  (definition 6's terminal expectation).
- `FXP_FIX_SILENT_LOGON_FAILURES=true|false`: explicit override of Logon policy.
- `FXP_FIX_WAIT_PEER_LOGOUT=true|false`: explicit override of fatal-close waiting.
- `FXP_FIX_LOGOUT_TIMEOUT` (existing, default `5s`): wait bound; a nonpositive
  configured timeout uses the five-second safety bound for fatal-error waiting.

Malformed preset/boolean values fail startup. During fatal-error waiting,
application messages are discarded; only the authenticated peer's Logout ends
the wait early. A timer always bounds the wait, including when the peer never
responds. The strict preset's heartbeat-timeout silent close does not wait for
an already-unresponsive peer. Both policy booleans can also be enabled directly
without changing the default diagnostic heartbeat-expiry policy.

Compose exposes these production policy settings; `FXP_FIX_TEST_FIXTURE` stays
hard-disabled and images contain only the normal binary. Existing deployments
need no configuration change. To opt in, set `FXP_FIX_COMPAT=quickfix` and restart
the bridge; use the explicit booleans if counterparties prefer mixed policies.
This work does not implement SecurityDefinition, TradeCaptureReport, initiator
mode, pegs, or the `35=H`/`35=AE` edge reply changes.

Store reset and session release share one critical section, preventing late
callbacks from advancing newly reset sequences. If scheduled archiving fails,
the old messages and sequence state are preserved rather than truncated; the
next scheduled Logon retries the archive and fails closed while it remains
unavailable. Operator intervention may be required to restore archive access.

Review regressions now cover large status records: valid records split before
exceeding the core-pool frame limit, while an individually oversized record
returns a bounded status rejection. This prevents status queries from taking
down the shared pool reader. FIX `MONTHYEAR` validation checks the entire
YYYYMM / YYYYMMDD / YYYYMMwN value (weeks 1–5), including real calendar dates.
Boolean enum violations and negative nonnegative-integer fields use reason 5;
leading-plus integer syntax is invalid and uses reason 6.
