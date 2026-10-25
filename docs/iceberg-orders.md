# Iceberg / reserve orders (FXP v2.4)

`NewOrderSingle.display_quantity` (FIX MaxFloor 111) is the maximum displayed
slice; `quantity` / FIX OrderQty is the total, including reserve. Zero or absent
means fully displayed. A positive display quantity must not exceed total OrderQty
and must be an integral multiple of the symbol's lot size. The minimum quantity
applies to the total, not each slice. The final residual slice may be smaller
than MaxFloor. Negative, fractional and overflowing display quantities are rejected.

Reserve is supported for limit DAY, GTC and GTD orders in continuous-only
sessions. Iceberg market, stop, stop-limit, IOC, FOK, AT_OPEN and AT_CLOSE orders
are rejected. Any symbol with a full auction schedule rejects iceberg entry and
replacement even during continuous trading, so a live iceberg cannot later enter
an auction. A close-only expiry schedule is supported; DAY/GTD expiry removes
both displayed and reserve quantity. Auction, stop and IOC/FOK behavior for
ordinary orders is unchanged.

## Matching and visibility

A slice participates in ordinary price/time priority. Each exhausted slice
replenishes immediately from reserve and moves to the **back** of its price
level, including behind other icebergs and ordinary orders. Each refresh gets a
new arrival sequence. Replenishment does not change the identity of the current
aggressor, its STP instruction or the passive execution price. Two icebergs can
match across multiple slices. An order is filled only when its total leaves reach
zero, and all its slices belong to one OrderID.

Snapshots and book incrementals expose only the current displayed quantity.
A fill removes displayed quantity and replenishment adds the fresh slice. Cancel
removes only the visible slice from market data and cancels the whole reserve.
Private execution reports and owner-scoped order status include total OrderQty,
CumQty and LeavesQty, with MaxFloor echoed when positive. Refresh is not a new
order acknowledgment. Fill reports are emitted per slice; their CumQty accumulates
against the total. Historical reports retain the configured display cap.

Ordinary IOC and market takers may consume multiple replenishments. An ordinary
FOK taker may consume reserve too. Its preflight follows first-rotation displayed
priority up to any STP conflict and counts complete reserve at conflict-free
levels. Any self-match before completion kills the whole FOK without touching the
book. Preflight is linear in resting orders, independent of the number of slices.
Execution still emits one fill per matched slice.

## Cancel/replace and STP

Replacement always loses priority, including reductions and display-only changes.
The new OrderQty is the new **total** including previous cumulative fills; it must
exceed CumQty. No quantity change retains total leaves. The old slice and reserve
are removed, then total minus CumQty is re-entered with a fresh displayed slice.
Price-only amendments preserve existing STP-restated totals, even below entry
minima, and retain a cap that now exceeds the restated total; a fresh slice is
still clamped to leaves. Explicit new totals and display caps are validated
against current entry constraints. An omitted MaxFloor keeps the cap; explicit `111=0` / optional
`new_display_quantity=0` disables reserve. Invalid replacements leave the original
slice, reserve and priority intact. Changing an iceberg to IOC/FOK or auction TIF
is rejected even if the replacement disables reserve; cancel and enter a new
ordinary order for those instructions. A cap may exceed current
leaves after fills; displayed quantity remains min(cap, leaves).

The aggressor's existing STP mode controls a self-match. CancelNewest,
CancelOldest and CancelBoth cancel the selected order's entire remaining reserve.
DecrementAndCancel reduces total leaves on both sides by the smaller total
leaves, consuming reserve first, then clamping displayed quantity to new leaves.
This is a non-trade restatement, retains priority and never increases CumQty.
STP grouping, owner isolation and cancel-on-disconnect remain unchanged.

## Wire and persistence

The additive v2.4 fields are NewOrderSingle tag 18 (`int32 display_quantity`),
OrderCancelReplaceRequest tag 13 (`optional int32 new_display_quantity`) and
ExecutionReport tag 20 (`int32 display_quantity`). Native clients must preserve
optional presence on replace. Gateway ownership and routing apply unchanged.
Input-log replay derives slices and refreshed priority deterministically from
persisted order inputs, including explicit zero versus absent display changes.
The replay format remains engine-input-v1, and legacy logs without symbol config
remain readable; changing a symbol configuration still fails startup.

Coverage includes FIFO across several icebergs, both matching paths, fills across
slices, partial cancel, replacement priority, IOC/FOK policy, all STP modes,
auction/stop rejection, replay and the randomized independent reference book.
`bash scripts/e2e.sh` runs `fix_test_client/order_types.py` through bridge, gateway
and core, alongside the existing FIX and persistence checks.
