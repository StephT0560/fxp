// SPDX-License-Identifier: MIT
import { formatPrice } from "../lib/format";
import { useSymbols, useTape } from "../lib/stores";

export function SymbolList() {
  const { list, selected, select } = useSymbols();
  useTape((s) => s.version);
  const tapes = useTape.getState().tapes;
  return (
    <nav className="w-44 shrink-0 overflow-y-auto border-r border-neutral-800 text-sm">
      {list.map((s) => {
        const t = tapes.get(s.symbol);
        return (
          <button
            key={s.symbol}
            data-testid={`symbol-${s.symbol}`}
            onClick={() => select(s.symbol)}
            className={`flex w-full justify-between px-3 py-1.5 text-left hover:bg-neutral-800 ${
              selected === s.symbol ? "bg-neutral-800 text-white" : "text-neutral-300"
            }`}
          >
            <span>{s.symbol}</span>
            <span className="num text-neutral-400">{formatPrice(t?.lastPrice, s.price_scale)}</span>
          </button>
        );
      })}
    </nav>
  );
}
