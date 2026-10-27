// SPDX-License-Identifier: MIT
import { OrderSide } from "../gen/fxp_pb";
import { formatPrice, formatQty } from "../lib/format";
import { MARKET_EMPTY } from "../lib/simulator";
import { useBook, useTape } from "../lib/stores";

export function TapeView({ symbol, scale }: { symbol: string; scale: number }) {
  useTape((s) => s.version);
  useBook((s) => s.version);
  const tape = useTape.getState().tapes.get(symbol);
  return (
    <section className="tape-panel flex min-h-0 shrink-0 flex-col border-l border-neutral-800" data-testid="tape">
      <div className="flex items-center justify-between border-b border-neutral-800 px-3 py-1 text-xs text-neutral-400">
        <span>Trades</span>
        <span
          data-testid="gap-counter"
          className={tape && tape.gapCount > 0 ? "rounded bg-red-900 px-1.5 text-red-200" : ""}
          title={tape?.lastGap ? `last gap: seq ${tape.lastGap.from}–${tape.lastGap.to}` : undefined}
        >
          {tape && tape.gapCount > 0
            ? `tape gap ×${tape.gapCount} (${tape.missed} missed)`
            : "gaps 0"}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {!useBook.getState().books.get(symbol)?.synced && !tape?.prints.length && <p className="p-3 text-sm text-neutral-400">{MARKET_EMPTY}</p>}
        {tape?.prints.map((p) => (
          <div key={p.key} className="grid grid-cols-4 gap-1 px-2 text-xs" data-testid="tape-row">
            <span className="num text-neutral-500">{new Date(p.timeMs).toLocaleTimeString("en-GB")}</span>
            <span
              className={`num ${
                p.aggressor === OrderSide.BUY ? "text-emerald-400" : p.aggressor === OrderSide.SELL ? "text-red-400" : "text-amber-300"
              }`}
            >
              {formatPrice(p.price, scale)}
            </span>
            <span className="num text-right">{formatQty(p.qty)}</span>
            <span className="num text-right text-neutral-500">{p.auction ? "AUC" : `#${p.seq}`}</span>
          </div>
        ))}
      </div>
    </section>
  );
}
