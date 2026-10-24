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
