# Market data

FXP publishes three per-symbol streams to every market-data subscriber of a
symbol (TCP `MarketDataRequest`, WebSocket subscription, or FIX `35=V` with
`263=1`): a full-depth `MarketDataSnapshot` on subscribe, then
`MarketDataIncrementalUpdate` level changes and the public trade feed
(`TradeCaptureReport`). All three share one droppable per-connection
market-data queue in the core; private `ExecutionReport`s use a separate
critical queue and are never dropped in favor of market data.

## Public trade feed (protocol v2.5)

The core emits exactly one `TradeCaptureReport` per fill, in matching order,
on the symbol's market-data path:

| Event | Prints |
|---|---|
| Single fill | one |
| Multi-level sweep | one per resting order matched |
| Iceberg | one per displayed slice actually matched; the reserve is never printed |
| Stop / stop-limit cascade | one per fill, in firing order (the triggering fills first, then each triggered stop's fills) |
| Auction uncross (open/close) | one per matched buy/sell pair, at the uncross price, `is_auction = true` |
| STP cancel or restatement (`EXEC_RESTATED`), cancel, cancel/replace without a cross, expiry, mass cancel, reject | none |

Fields:

| Field | No. | Public value |
|---|---|---|
| `header` | 1 | `protocol_version = "2.5"`, `sequence_number = 0` |
| `trade_id` | 2 | `<SYMBOL>:<seq>` |
| `buy_order_id`, `sell_order_id` | 3, 4 | always empty |
| `symbol` | 5 | instrument |
| `trade_price` | 6 | fixed-point price in the symbol's scale |
| `trade_quantity` | 7 | matched quantity |
| `timestamp` | 8 | engine time of the fill |
| `aggressor_side` | 9 | side of the incoming (taking) order; `ORDER_SIDE_UNSPECIFIED` for auction prints, which have no aggressor |
| `seq` | 10 | per-symbol public sequence, see below |
| `is_auction` | 11 | `true` only for auction uncross prints |

Fields 9-11 are additive (new field numbers); v2.4 decoders ignore them.

### Privacy

The feed is anonymous. A trade report never carries an order id, owner,
session, account or `ClOrdID`: `buy_order_id`/`sell_order_id` are empty and
the message has no other identifying field. Participants learn about their own
fills only from their private `ExecutionReport`s, which are unchanged.

### Sequence and gap contract

- `seq` is assigned by the symbol's engine task when the print is emitted: it
  starts at 1 and increases by exactly 1 per print for that symbol. Symbols are
  numbered independently.
- Numbering is deterministic. With `FXP_EVENT_LOG` configured, a restarted
  core replays the journal and continues at the same `seq`.
- Delivery is best effort. When a subscriber's market-data queue is full, the
  report is dropped and counted like any other market data (the existing
  market-data drop counter). The emitter never skips a number, so a jump in
  `seq` seen by a client means that client missed prints. Recover by
  re-subscribing (fresh snapshot); missed prints are not replayed.
- `seq` is not reset by session phases or by a client's re-subscription.
- Without `FXP_EVENT_LOG`, a core restart starts numbering again at 1.

The Go test clients (`go_gateway/test_clients/tcp_client`, `ws_client`) print
each report and log `SEQ GAP symbol=.. expected=.. got=..` on a jump.

## FIX mapping

The FIX bridge sends each trade report to every active streaming market-data
request (`263=1`) for the symbol as a `35=X` MarketDataIncrementalRefresh with
one entry:

| Tag | Value |
|---|---|
| `262` MDReqID | the subscriber's request id |
| `268` NoMDEntries | `1` |
| `279` MDUpdateAction | `0` (New) |
| `269` MDEntryType | `2` (Trade) |
| `55` Symbol | symbol |
| `270` MDEntryPx | price formatted with the symbol's configured scale |
| `271` MDEntrySize | quantity |
| `273` MDEntryTime | fill time, UTC `HH:MM:SS.sss` |

FIX 4.4 has no field for the aggressor side or the FXP public sequence, so
both are omitted. FIX clients detect gaps only through the FIX session
sequence. Trades go to every streaming request for the symbol, whether or not
its `267` group lists `269=2`; the requested entry types are not used to filter
(as for bids/offers today). Snapshot-only requests (`263=0`) do not receive
trades.
