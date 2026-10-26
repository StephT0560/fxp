// SPDX-License-Identifier: MIT
import { DEPTH, isCrossed, topAsks, topBids, type Level } from "../lib/book";
import { formatPrice, formatQty } from "../lib/format";
import { useBook } from "../lib/stores";

function Row({ level, side, scale, max }: { level?: Level; side: "bid" | "ask"; scale: number; max: number }) {
  const pct = level && max > 0 ? Math.round((level.qty / max) * 100) : 0;
  const color = side === "bid" ? "text-emerald-400" : "text-red-400";
  const bar = side === "bid" ? "bg-emerald-900/40" : "bg-red-900/40";
  return (
    <div className="relative grid h-6 grid-cols-2 items-center px-3 text-sm" data-testid={level ? `${side}-row` : undefined}>
      <div className={`absolute inset-y-0 right-0 ${bar}`} style={{ width: `${pct}%` }} />
      <span className={`num relative ${color}`}>{level ? formatPrice(level.price, scale) : ""}</span>
      <span className="num relative text-right">{level ? formatQty(level.qty) : ""}</span>
    </div>
  );
}

export function Ladder({ symbol, scale }: { symbol: string; scale: number }) {
  useBook((s) => s.version);
  const book = useBook.getState().books.get(symbol);
  const bids = book ? topBids(book, DEPTH) : [];
  const asks = book ? topAsks(book, DEPTH) : [];
  const max = Math.max(1, ...bids.map((l) => l.qty), ...asks.map((l) => l.qty));
  const crossed = isCrossed(bids, asks);
  const spread = bids[0] && asks[0] ? asks[0].price - bids[0].price : undefined;
  return (
    <section className="flex w-72 shrink-0 flex-col overflow-y-auto border-r border-neutral-800" data-testid="ladder">
      <div className="flex justify-between border-b border-neutral-800 px-3 py-1 text-xs text-neutral-400">
        <span>Depth · {symbol}</span>
        {!book?.synced && <span className="text-amber-400" data-testid="book-unsynced">awaiting snapshot</span>}
        {crossed && <span className="text-red-400" data-testid="book-crossed">crossed</span>}
      </div>
      {Array.from({ length: DEPTH }, (_, i) => (
        <Row key={`a${i}`} level={asks[DEPTH - 1 - i]} side="ask" scale={scale} max={max} />
      ))}
      <div className="num border-y border-neutral-800 px-3 py-1 text-center text-xs text-neutral-400">
        spread {formatPrice(spread, scale)}
      </div>
      {Array.from({ length: DEPTH }, (_, i) => (
        <Row key={`b${i}`} level={bids[i]} side="bid" scale={scale} max={max} />
      ))}
    </section>
  );
}
