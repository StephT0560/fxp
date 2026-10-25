# Symbol prices and quantities

`FXP_SYMBOL_CONFIG` is an optional CSV path loaded once by the core and FIX
bridge, before accepting connections. Both processes must use identical files.
The gateway transports integer prices unchanged; native clients need the same
scale definitions. A missing file, malformed row, duplicate symbol or invalid
bounds fails startup. No live reload is supported.

```csv
symbol,price_scale,tick_size,lot_size,min_quantity,max_quantity
P0,0,1,1,1,2147483647
P2,2,5,10,10,1000000
P4,4,1,1,1,1000000
P8,8,1,1,1,1000000
```

Blank lines and lines starting with `#` are ignored; the exact header is
optional. Each data row contains exactly six comma-separated fields (no CSV
quoting). Symbols use ASCII letters/digits or `._-/:`. All five numeric fields
are unsigned decimal integers. Tick size is **minor units**, not a decimal
price: P2's tick 5 is 0.05. Scale is 0–8. Tick/lot/minimum must be positive;
minimum ≤ maximum ≤ INT32_MAX, with at least one lot multiple in the interval.

Unlisted symbols keep scale 2, tick 1 (0.01), lot/minimum 1 and maximum
2,147,483,647. Only limit/stop-limit prices and stop/stop-limit triggers are validated for ticks;
unused price fields on market and stop-market orders retain their ignored
semantics. Positive active prices must be tick multiples; order
total quantity must be within min/max and a lot multiple. Replace uses the same
rules before changing the book; zero change fields retain their existing
meaning. Invalid replaces leave the original order intact. Matching permits
fills below the minimum order size; the minimum applies to order entry, not
individual trades. Quantities and display slices remain integer units.

FIX prices for explicitly configured symbols are exact: excess nonzero
fractional digits are rejected (extra trailing zeros are accepted). The legacy
unlisted-symbol path retains two-decimal half-away-from-zero rounding.
Execution reports, snapshots and incrementals format using the symbol scale.
Integer parsing/formatting avoids binary floating point and handles signed
int64 bounds, including negative values used in rejection/formatting tests;
accepted order prices must be positive. The old bridge's accidental int32
price ceiling is removed to honor the wire's int64 representation.

## Overflow headroom

The largest minor-unit price is 9,223,372,036,854,775,807. The largest accepted
price is this value rounded **down** to a tick multiple. With tick 1:

| Scale | Maximum decimal price |
|---|---|
| 0 | 9223372036854775807 |
| 1 | 922337203685477580.7 |
| 2 | 92233720368547758.07 |
| 3 | 9223372036854775.807 |
| 4 | 922337203685477.5807 |
| 5 | 92233720368547.75807 |
| 6 | 9223372036854.775807 |
| 7 | 922337203685.4775807 |
| 8 | 92233720368.54775807 |

Cumulative trade notional uses signed i128: INT64_MAX × INT32_MAX remains
below its limit, and average price remains int64. Tests cover scales 0/2/4/8,
the maximum value and one minor unit beyond it, strict precision and legacy
rounding, and formatting INT64_MIN without negation overflow.

The event-log header fingerprints the canonical symbol configuration. Replay
with a changed scale/tick/lot/quantity rule fails closed instead of interpreting
old minor units or accepted quantities under new rules. Existing logs with
no symbol configuration retain their original header and remain readable.
Changing configuration for a persisted deployment needs an explicit migration
or a fresh log; never silently reuse a log with different symbol rules.
