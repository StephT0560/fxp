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
invalid enums 5, incorrect datatypes 6, duplicate tags 13, group order 15 and
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
The response is batched in groups of at most 64 reports and supplies completion
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
