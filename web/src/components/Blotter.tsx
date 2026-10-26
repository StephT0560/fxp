// SPDX-License-Identifier: MIT
import { useState } from "react";
import { OrderSide, OrderType, TimeInForce } from "../gen/fxp_pb";
import { activeClient } from "../lib/client";
import { formatPrice, formatQty } from "../lib/format";
import { isTerminal, type OrderRow, type OrderState } from "../lib/orders";
import { useOrders, useSymbols } from "../lib/stores";
import { parsePrice, parseQty, type SymbolRules } from "../lib/validate";

type Tab = "working" | "completed" | "fills" | "log";

const stateColor: Record<OrderState, string> = {
  pending: "text-amber-300",
  new: "text-sky-300",
  partial: "text-sky-200",
  filled: "text-emerald-400",
  canceled: "text-neutral-400",
  rejected: "text-red-400",
  expired: "text-neutral-500",
  replaced: "text-neutral-500",
};
const typeName = (t?: OrderType) => (t === undefined ? "—" : (OrderType[t] ?? "").toLowerCase().replace("_", " "));
const tifName = (t?: TimeInForce) => (t === undefined ? "" : (TimeInForce[t] ?? "").replace("_", " "));
const fallback: SymbolRules = { symbol: "", price_scale: 2, tick_size: "1" };

function useRules(): (symbol: string) => SymbolRules {
  const list = useSymbols((s) => s.list);
  return (symbol) => list.find((s) => s.symbol === symbol) ?? { ...fallback, symbol };
}

function ReplaceModal({ row, rules, onClose }: { row: OrderRow; rules: SymbolRules; onClose: () => void }) {
  const [price, setPrice] = useState(row.price !== undefined ? formatPrice(row.price, rules.price_scale) : "");
  const [qty, setQty] = useState(String(row.qty));
  const [display, setDisplay] = useState(row.displayQty ? String(row.displayQty) : "");
  const hasPrice = row.price !== undefined || row.type === undefined;
  const p = hasPrice && price.trim() !== "" ? parsePrice(price, rules) : undefined;
  const q = parseQty(qty, rules);
  const d = display.trim() === "" ? undefined : display.trim() === "0" ? 0 : parseQty(display, { ...rules, min_quantity: 1 });
  // Quantity against the filled amount is left to the core so its reject is shown.
  const errors = [typeof p === "string" && `Price ${p}`, typeof q === "string" && `Quantity ${q}`, typeof d === "string" && `Display ${d}`].filter(Boolean);
  const submit = () => {
    if (errors.length > 0) return;
    activeClient.current?.replace(row, {
      price: typeof p === "bigint" && p !== row.price ? p : undefined,
      qty: typeof q === "number" && q !== row.qty ? q : undefined,
      displayQty: typeof d === "number" && d !== row.displayQty ? d : undefined,
    });
    onClose();
  };
  return (
    <div className="fixed inset-0 z-10 flex items-center justify-center bg-black/60" data-testid="replace-modal">
      <form
        className="flex w-72 flex-col gap-2 rounded border border-neutral-700 bg-neutral-900 p-4 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <div className="font-medium">
          Replace {row.orderId} · {row.symbol}
        </div>
        <div className="text-xs text-neutral-400">
          filled {row.cum} of {row.qty}
        </div>
        {hasPrice && (
          <label className="text-xs text-neutral-400">
            new price
            <input data-testid="replace-price" className="num w-full rounded bg-neutral-800 px-1" value={price} onChange={(e) => setPrice(e.target.value)} />
          </label>
        )}
        <label className="text-xs text-neutral-400">
          new total quantity
          <input data-testid="replace-qty" className="num w-full rounded bg-neutral-800 px-1" value={qty} onChange={(e) => setQty(e.target.value)} />
        </label>
        <label className="text-xs text-neutral-400">
          display qty
          <input data-testid="replace-display" className="num w-full rounded bg-neutral-800 px-1" value={display} onChange={(e) => setDisplay(e.target.value)} />
        </label>
        {errors.length > 0 && (
          <ul className="text-xs text-red-400" data-testid="replace-errors">
            {errors.map((e) => (
              <li key={String(e)}>{e}</li>
            ))}
          </ul>
        )}
        <div className="flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded bg-neutral-800 px-2 py-1" data-testid="replace-close">
            Close
          </button>
          <button type="submit" disabled={errors.length > 0} className="rounded bg-blue-700 px-2 py-1 disabled:opacity-40" data-testid="replace-submit">
            Replace
          </button>
        </div>
      </form>
    </div>
  );
}

function OrderRows({ rows, working }: { rows: OrderRow[]; working: boolean }) {
  const rules = useRules();
  const [replacing, setReplacing] = useState<OrderRow>();
  return (
    <>
      <table className="w-full text-sm">
        <thead className="text-xs text-neutral-500">
          <tr className="text-left">
            <th className="px-2">id</th>
            <th>sym</th>
            <th>side</th>
            <th>type</th>
            <th className="text-right">price</th>
            <th className="text-right">qty</th>
            <th className="text-right">cum</th>
            <th className="text-right">leaves</th>
            <th className="text-right">avg px</th>
            <th className="px-2">state</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const scale = rules(r.symbol).price_scale;
            return (
              <tr key={r.orderId} data-testid="order-row" data-order-id={r.orderId} data-state={r.state} className="border-t border-neutral-900">
                <td className="num px-2 text-neutral-500">{r.orderId}</td>
                <td>{r.symbol}</td>
                <td className={r.side === OrderSide.BUY ? "text-emerald-400" : "text-red-400"}>{r.side === OrderSide.BUY ? "buy" : "sell"}</td>
                <td className="text-neutral-400">
                  {typeName(r.type)} {tifName(r.tif)}
                  {r.displayQty > 0 ? ` ice ${r.displayQty}` : ""}
                </td>
                <td className="num text-right" data-testid="order-price">
                  {r.price !== undefined ? formatPrice(r.price, scale) : r.stopPrice !== undefined ? `stop ${formatPrice(r.stopPrice, scale)}` : "—"}
                </td>
                <td className="num text-right" data-testid="order-qty">{formatQty(r.qty)}</td>
                <td className="num text-right" data-testid="order-cum">{formatQty(r.cum)}</td>
                <td className="num text-right" data-testid="order-leaves">{formatQty(r.leaves)}</td>
                <td className="num text-right">{r.cum > 0 ? formatPrice(r.avgPx, scale) : "—"}</td>
                <td className="px-2">
                  <span className={stateColor[r.state]} data-testid="order-state">
                    {r.state}
                    {r.restated ? " (restated)" : ""}
                    {r.pending ? ` · ${r.pending}…` : ""}
                  </span>
                  {(r.requestReject || r.reason) && (
                    <div className="text-xs text-red-300" data-testid="order-reason">
                      {r.requestReject ?? r.reason}
                    </div>
                  )}
                </td>
                <td className="whitespace-nowrap text-right">
                  {working && !isTerminal(r.state) && (
                    <>
                      <button
                        data-testid="order-replace"
                        disabled={r.pending !== undefined || r.state === "pending"}
                        onClick={() => setReplacing(r)}
                        className="mr-1 rounded bg-neutral-800 px-1.5 text-xs hover:bg-neutral-700 disabled:opacity-40"
                      >
                        Replace
                      </button>
                      <button
                        data-testid="order-cancel"
                        disabled={r.pending !== undefined || r.state === "pending"}
                        onClick={() => activeClient.current?.cancel(r)}
                        className="rounded bg-neutral-800 px-1.5 text-xs hover:bg-neutral-700 disabled:opacity-40"
                      >
                        Cancel
                      </button>
                    </>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {replacing && <ReplaceModal row={replacing} rules={rules(replacing.symbol)} onClose={() => setReplacing(undefined)} />}
    </>
  );
}

function OwnFills() {
  const rules = useRules();
  const fills = useOrders.getState().blotter.fills;
  return (
    <div data-testid="own-fills">
      {fills.length === 0 && <div className="px-2 text-xs text-neutral-500">No fills yet. Public prints are on the trade tape.</div>}
      {fills.map((f) => (
        <div key={f.key} className="grid grid-cols-6 px-2 text-sm" data-testid="own-fill-row">
          <span className="num text-neutral-500">{new Date(f.timeMs).toLocaleTimeString("en-GB")}</span>
          <span>{f.symbol}</span>
          <span className={f.side === OrderSide.BUY ? "text-emerald-400" : "text-red-400"}>{f.side === OrderSide.BUY ? "bought" : "sold"}</span>
          <span className="num text-right">{formatQty(f.qty)}</span>
          <span className="num text-right">@ {formatPrice(f.price, rules(f.symbol).price_scale)}</span>
          <span className="text-right text-xs text-neutral-500">{f.auction ? "auction" : f.orderId}</span>
        </div>
      ))}
    </div>
  );
}

function ExecLog() {
  const log = useOrders.getState().blotter.log;
  const color = { report: "text-neutral-300", request: "text-neutral-500", reject: "text-red-400", info: "text-sky-300" } as const;
  return (
    <div data-testid="exec-log" className="font-mono text-xs">
      {log.map((e) => (
        <div key={e.key} className={`px-2 ${color[e.kind]}`} data-testid="exec-log-row">
          <span className="text-neutral-600">{new Date(e.timeMs).toLocaleTimeString("en-GB")}</span> {e.text}
        </div>
      ))}
    </div>
  );
}

export function Blotter({ symbol }: { symbol: string }) {
  useOrders((s) => s.version);
  const notice = useOrders((s) => s.notice);
  const { blotter } = useOrders.getState();
  const [tab, setTab] = useState<Tab>("working");
  const [scope, setScope] = useState<"all" | "symbol">("all");
  const rows = [...blotter.rows.values()].sort((a, b) => b.updated - a.updated);
  const working = rows.filter((r) => !isTerminal(r.state));
  const completed = rows.filter((r) => isTerminal(r.state));
  const tabs: [Tab, string][] = [
    ["working", `Working (${working.length})`],
    ["completed", `Completed (${completed.length})`],
    ["fills", `Own fills (${blotter.fills.length})`],
    ["log", "Exec reports"],
  ];
  return (
    <section className="flex min-w-0 flex-1 flex-col overflow-hidden" data-testid="blotter">
      <div className="flex flex-wrap items-center gap-1 border-b border-neutral-800 px-2 py-1 text-xs">
        {tabs.map(([t, label]) => (
          <button
            key={t}
            data-testid={`blotter-tab-${t}`}
            onClick={() => setTab(t)}
            className={`rounded px-2 py-0.5 ${tab === t ? "bg-neutral-700 text-neutral-100" : "text-neutral-400 hover:bg-neutral-800"}`}
          >
            {label}
          </button>
        ))}
        {blotter.stale && (
          <span className="text-amber-400" data-testid="blotter-stale">
            resyncing from order status…
          </span>
        )}
        <span className="ml-auto truncate text-neutral-400" data-testid="blotter-notice">
          {notice}
        </span>
        <select data-testid="cancel-all-scope" value={scope} onChange={(e) => setScope(e.target.value as "all" | "symbol")} className="rounded bg-neutral-800 px-1">
          <option value="all">all symbols</option>
          <option value="symbol">{symbol} only</option>
        </select>
        <button
          data-testid="cancel-all"
          onClick={() => activeClient.current?.cancelAll(scope === "symbol" ? symbol : undefined)}
          className="rounded bg-red-900 px-2 py-0.5 text-red-100 hover:bg-red-800"
        >
          Cancel all
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "working" && <OrderRows rows={working} working />}
        {tab === "completed" && <OrderRows rows={completed} working={false} />}
        {tab === "fills" && <OwnFills />}
        {tab === "log" && <ExecLog />}
      </div>
    </section>
  );
}
