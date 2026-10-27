// SPDX-License-Identifier: MIT
import { OrderSide } from "../gen/fxp_pb";
import { isTerminal, type OrderRow } from "./orders";
import type { Level } from "./book";

/** Prices come only from locally known order rows; status reports cannot supply them. */
export function ownLevels(rows: Iterable<OrderRow>, symbol: string, bids: Level[], asks: Level[]) {
  const buy = new Map<bigint, number>();
  const sell = new Map<bigint, number>();
  const visibleBuy = new Set(bids.map((l) => l.price));
  const visibleSell = new Set(asks.map((l) => l.price));
  let unknown = 0, offLadder = 0;
  for (const row of rows) {
    if (row.symbol !== symbol || isTerminal(row.state) || row.leaves <= 0) continue;
    if (row.price === undefined) { unknown++; continue; }
    if (row.side !== OrderSide.BUY && row.side !== OrderSide.SELL) continue;
    const levels = row.side === OrderSide.BUY ? buy : sell;
    levels.set(row.price, (levels.get(row.price) ?? 0) + row.leaves);
    if (!(row.side === OrderSide.BUY ? visibleBuy : visibleSell).has(row.price)) offLadder++;
  }
  return { buy, sell, unknown, offLadder };
}
