// SPDX-License-Identifier: MIT
import { priceMovement } from "../lib/movement";
import { formatPrice } from "../lib/format";
import { useSymbols, useTape } from "../lib/stores";

export function SymbolList() {
  const { list, selected, select } = useSymbols();
  useTape((s) => s.version);
  const tapes = useTape.getState().tapes;
  return (
    <nav className="market-symbols shrink-0 overflow-y-auto border-r border-neutral-800 text-sm">
      {list.map((s) => {
        const t = tapes.get(s.symbol);
        const movement = priceMovement(t?.lastPrice, t?.previousPrice, t?.firstPrice);
        return (
          <button
            key={s.symbol}
            data-testid={`symbol-${s.symbol}`}
            onClick={() => select(s.symbol)}
            className={`flex w-full flex-col px-3 py-1.5 text-left hover:bg-neutral-800 ${
              selected === s.symbol ? "bg-neutral-800 text-white" : "text-neutral-300"
            }`}
          >
            <span className="flex justify-between gap-1 text-xs 2xl:text-sm"><span>{s.symbol}</span><span data-testid="symbol-last" data-direction={movement.direction} className={`num ${movement.direction > 0 ? "text-emerald-400" : movement.direction < 0 ? "text-red-400" : "text-neutral-400"}`}>{formatPrice(t?.lastPrice, s.price_scale)}</span></span>
            {movement.change !== undefined && <span className="text-right text-[10px] leading-tight text-neutral-500" title="Change since first print seen by this console, not daily or session-open change">{movement.change > 0n ? "+" : ""}{formatPrice(movement.change, s.price_scale)} <span className="text-[10px]">since first print seen</span></span>}
          </button>
        );
      })}
    </nav>
  );
}
