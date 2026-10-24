# Risk controls: self-trade prevention, mass cancel, cancel-on-disconnect

FXP v2.2 adds three pre-trade/session risk controls. All are off by default;
a client that sends none of the new fields sees exactly the v2.1 behavior.

## Self-trade prevention (STP)

A *self-match* is a potential fill where both orders have the same STP key.
The key is the order's STP group (`NewOrderSingle.stp_group`, FIX tag 2362)
when set, otherwise the owner (the gateway-authenticated JWT `sub`). The
gateway namespaces groups per trader (`<sub>::<group>`), so one trader can
never name another trader's group.

The **aggressor's** (incoming order's) instruction decides what happens:

| FXP `stp_mode` | FIX 2964 | Effect on a self-match |
|---|---|---|
| `UNSPECIFIED` (0) | absent | use the engine default `FXP_STP_DEFAULT` |
| `NONE` | — | match normally (explicit opt-out) |
| `CANCEL_NEWEST` | `1` (cancel aggressive) | cancel the aggressor's remaining quantity |
| `CANCEL_OLDEST` | `2` (cancel passive) | cancel the resting order, keep matching |
| `CANCEL_BOTH` | `3` | cancel both |
| `DECREMENT_AND_CANCEL` | `99` (FXP extension) | reduce both by the smaller quantity; the smaller (both if equal) is canceled, the survivor gets an `EXEC_RESTATED` report (FIX `150=D`) |

`FXP_STP_DEFAULT` accepts `none` (default), `cancel-newest`, `cancel-oldest`,
`cancel-both`, `decrement-and-cancel`; an invalid value stops the core at
startup. Tags 2362 (`SelfMatchPreventionID`) and 2964
(`SelfMatchPreventionInstruction`) are the FIX 5.0 SP2 tags, confirmed in the
QuickFIX `FIX50SP2.xml` dictionary; 2964 values other than 1, 2, 3 and 99 are
rejected.

Guarantees:

- A prevented match prints no trade and emits no trade market-data update;
  each canceled order gets `EXEC_CANCELED` with text
  `self-trade prevention (<mode>)`. Book-level updates for removed resting
  quantity are emitted as for any cancel.
- Partially filled resting orders keep their fills; only the open quantity is
  canceled or decremented.
- IOC: a prevented match ends like any other unfilled IOC remainder.
- FOK: if filling the order would require a self-match, the FOK is canceled
  with the STP text and nothing fills (no partial fill).
- Stops and stop-limits: STP is checked when a triggered stop matches, using
  the stop's own instruction (it is the aggressor).
- Cancel/replace: the replacement keeps the original's STP settings and is
  checked as a new aggressor.
- Auctions: an auction-only order (`AT_OPEN`/`AT_CLOSE`) with an explicit STP
  mode is rejected; with `UNSPECIFIED` it is accepted. Auction uncrossing
  ignores STP — orders resting in a call book can be uncrossed against the
  same owner. This is the simplest safe behavior: no auction allocation is
  silently changed, and clients that need STP must avoid auction TIFs.
- Event-log persistence records the configured default; a log written with a
  different `FXP_STP_DEFAULT` is refused on replay.

## Mass cancel

`OrderMassCancelRequest {sender, symbol?, side?, client_request_id}` cancels
every live order of `sender` — resting, parked stops and held auction orders
— optionally restricted to one symbol and/or side. The gateway always
overwrites `sender` with the authenticated trader, so a caller can only
cancel its own orders. Each canceled order gets `EXEC_CANCELED` with text
`mass cancel`, then the core answers `OrderMassCancelReport` with
`total_affected` (0 for an empty result) or a non-empty `reject_reason`.

FIX `35=q` (required 11, 530, 60):

| 530 MassCancelRequestType | Meaning |
|---|---|
| `7` | all orders (optionally `54` side) |
| `1` | orders for `55` Symbol (required), optionally `54` side |
| other | `35=r` with `531=0`, `532=0` (not supported) and `58` text |

The answer is `35=r` with `11`/`37` = ClOrdID, `530`, `531` (echoes 530, or
`0` when rejected), `532` (when rejected: `1` missing symbol, `99` other),
`533` total affected, plus one `35=8 150=4` per canceled order. The report
and the individual ERs travel on different core connections, so `35=r` may
arrive before some of the ERs. ERs are delivered to the connection that
entered each order.

## Cancel-on-disconnect (COD)

Opt in per session: FXP `Logon.cancel_on_disconnect = true`, or FIX Logon tag
**8013=Y** (FXP user-defined; `N` or absent = off, any other value rejects
the Logon). When a COD session ends for any reason — Logout, TCP close or
reset, heartbeat timeout, or the bridge's link to the gateway failing — an
owner-scoped mass cancel with reason `DISCONNECT` is sent to the core; the
ERs carry the text `cancel on disconnect`. A repeated Logon on a live gateway
connection is not a disconnect.

FIX durability: the bridge issues the COD mass cancel itself after the FIX
connection closed and before closing its gateway link, and waits (up to 2 s)
for the report and all its ERs. The ERs go through the normal outbound path,
so for a FileStore session they are stored with sequence numbers and are
returned by a ResendRequest after the client reconnects. The gateway's own
COD (the bridge always logs on with the session's COD flag) is the backstop
when the bridge-gateway link itself fails; in that case the cancels happen
but their ERs cannot reach the bridge and are not in the FileStore.
