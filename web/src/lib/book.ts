// SPDX-License-Identifier: MIT
import type { MarketDataIncrementalUpdate, MarketDataSnapshot } from "../gen/fxp_pb";
import { OrderSide } from "../gen/fxp_pb";

export const DEPTH = 10;

/** Aggregated quantity per price (prices are scaled int64 ticks). */
export type Levels = Map<bigint, number>;

export interface Book {
  symbol: string;
  bids: Levels;
  asks: Levels;
  /** False until a snapshot has been applied; incrementals are dropped before that. */
  synced: boolean;
  /** Updates that referenced quantity the book did not have (out-of-order / lost MD). */
  anomalies: number;
}

export interface Level {
  price: bigint;
  qty: number;
}

export function emptyBook(symbol: string): Book {
  return { symbol, bids: new Map(), asks: new Map(), synced: false, anomalies: 0 };
}

export function applySnapshot(book: Book, snap: MarketDataSnapshot): void {
  book.bids = new Map();
  book.asks = new Map();
  for (const l of snap.bidLevels) if (l.size > 0) book.bids.set(l.price, (book.bids.get(l.price) ?? 0) + l.size);
  for (const l of snap.askLevels) if (l.size > 0) book.asks.set(l.price, (book.asks.get(l.price) ?? 0) + l.size);
  book.synced = true;
}

/**
 * Applies an order-level delta: update_type 0 (New) adds quantity at the
 * level, 1 (Modify/fill) and 2 (Cancel) remove it. Returns false when the
 * update was ignored because no snapshot has been applied yet.
 */
export function applyIncremental(book: Book, inc: MarketDataIncrementalUpdate): boolean {
  if (!book.synced) return false;
  for (const c of inc.changes) {
    const side = c.side === OrderSide.BUY ? book.bids : c.side === OrderSide.SELL ? book.asks : undefined;
    if (!side || c.quantity < 0) {
      book.anomalies++;
      continue;
    }
    const cur = side.get(c.price) ?? 0;
    if (c.updateType === 0) {
      side.set(c.price, cur + c.quantity);
      continue;
    }
    if (c.updateType !== 1 && c.updateType !== 2) {
      book.anomalies++;
      continue;
    }
    const next = cur - c.quantity;
    if (next < 0) book.anomalies++;
    if (next <= 0) side.delete(c.price);
    else side.set(c.price, next);
  }
  return true;
}

function top(levels: Levels, n: number, desc: boolean): Level[] {
  const prices = [...levels.keys()].sort((a, b) => (a === b ? 0 : (a < b) === desc ? 1 : -1));
  return prices.slice(0, n).map((price) => ({ price, qty: levels.get(price) ?? 0 }));
}

export function topBids(book: Book, n = DEPTH): Level[] {
  return top(book.bids, n, true);
}

export function topAsks(book: Book, n = DEPTH): Level[] {
  return top(book.asks, n, false);
}

/** True when best bid >= best ask, which must be surfaced rather than hidden. */
export function isCrossed(bids: Level[], asks: Level[]): boolean {
  const b = bids[0];
  const a = asks[0];
  return b !== undefined && a !== undefined && b.price >= a.price;
}
