# Pegged orders: Stage C design (not implemented)

Stage A symbol precision and Stage B reserve orders are implemented. Stage C is
explicitly deferred using the task's permitted stopping point. No peg wire fields,
FIX mappings or matching behavior are partially implemented in this change.
ExecInst 18 R/P/M and PegDifference 211 remain unsupported.

## References and price calculation

Maintain a separate reference BBO derived only from **non-pegged displayed limit
orders**. Exclude all pegs, including capped and parked pegs, from references.
Iceberg references use their currently displayed slices; reserve is never a
reference. This single rule prevents direct and indirect peg cycles, including
opposite-side pegs and self-referencing reprices.

Primary peg: same-side reference (buy best bid, sell best ask). Market peg:
opposite-side reference (buy best ask, sell best bid). Midpoint peg: arithmetic
midpoint of both references, requiring both sides. Offset is signed minor units,
added to the reference on both sides. For an odd midpoint or off-tick result,
round toward the less aggressive valid tick (buy down, sell up). Then apply an
optional limit cap: buy may not exceed it, sell may not fall below it. Caps and
offsets use the Stage A symbol scale and checked i128 arithmetic; reject overflow,
nonpositive effective price, and invalid caps before mutating the order.

If required references are missing, park the peg with no visible quantity. Keep
its accepted OrderID, total leaves and private status; activate only when all
required references become available. Initial acceptance acknowledges a parked
working order. Cancel/replace and owner-scoped status must work while parked.
A disappearing reference withdraws its visible order and parks it again.

## Atomic updates and priority

After each atomic ordinary-book mutation (entry, trade, reserve refresh, cancel,
replace, expiry or STP), recompute the non-peg reference BBO. Reprice affected pegs
in deterministic acceptance-ID order. Remove/reinsert only when the effective
price changes or a parked peg activates; each reinsert receives new time priority.
Unchanged capped prices retain priority. Match using existing STP and report
rules, then process further non-peg reference changes until stable. Pure peg
mutations cannot alter reference prices. Use a work queue with a monotonic book
revision and an explicit processing bound; never recursive repricing. On hitting
that bound, fail closed and halt the symbol rather than silently dropping reprices.

Trades against pegs can consume non-peg references and cause another reference
revision, so excluding pegs alone does not bound the number of fills. All work
must remain deterministic and persisted/replayed from the same external input and
clock. An acceptance-rate/order-quantity work budget needs measurement before
choosing its production bound; this is an implementation prerequisite.

## Wire and interaction policy

Add an enum with UNSPECIFIED (ordinary), PRIMARY, MARKET and MIDPOINT and optional
signed offset/cap fields, at new permanent protobuf numbers, in the next minor
protocol version. FIX uses ExecInst 18 R/P/M, PegDifference 211 and optional Price
44 as cap, with a limit order type. Preserve optional presence in replacements;
unspecified fields retain their old values, and an explicit peg-type change can
convert between ordinary and pegged limits. Replace always loses priority,
retains cumulative fills and revalidates the entire instruction atomically.
Execution reports and status echo peg instruction, offset, cap and working price.

Initially support only fully displayed limit DAY/GTC/GTD pegs in symbols without
full auctions. Reject peg-plus-iceberg, stop/stop-limit, market, IOC/FOK and auction
combinations. Ordinary IOC/FOK takers may trade active pegs, but their FOK preflight
must simulate any reference withdrawals and reprices before committing fills.
This must be implemented before enabling pegs; the current iceberg FOK preflight
is insufficient for dynamically repricing orders. Existing aggressor STP modes
apply; a repriced peg is a new aggressor for matching and cannot bypass ownership
or group checks. Close-only expiry cancels live and parked DAY/GTD pegs.

## Required tests before enabling

- Primary/market/midpoint on both sides; reference up/down, signed offsets,
  off-tick/odd midpoints, scale 0/2/4/8, overflow and cap binding/release.
- Missing/one-sided book, reference cancellation, park/reactivate and ownership
  isolation for active, parked and terminal status.
- Opposite-side pegs and multiple same-side pegs never referencing each other;
  ordinary reference removal during peg fills converges deterministically.
- Every actual reprice loses FIFO priority; unchanged capped prices retain it;
  cancel/replace of active and parked pegs preserves cumulative quantities.
- STP modes, ordinary IOC/FOK execution, rejected stop/auction/reserve combinations,
  expiry, cancel-on-disconnect and exact durable replay of reprice ordering.
- Extend the independent linear reference model with separate non-peg BBO and a
  deterministic peg work queue; compare fills, parked state, private totals,
  displayed levels and priority after every generated operation.
- Full bridge/gateway/core FIX integration and all existing conformance gates.
