// SPDX-License-Identifier: MIT
import { useState } from "react";
import { useInspector, type InspectedFrame } from "../lib/inspector";
import { parseFixPreview } from "../lib/fix-preview";
import { buttonFocus, neutralButton } from "../lib/styles";

type FixPreview = { fix: string; fix_bytes: number; fxp_bytes: number; note: string };
const control = `rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100 ${buttonFocus}`;
const block = "rounded border border-neutral-800 bg-neutral-900/50 p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-all";

export function ProtocolInspector() {
  useInspector((s) => s.version);
  const rows = useInspector.getState().rows;
  const [type, setType] = useState("");
  const [symbol, setSymbol] = useState("");
  const [direction, setDirection] = useState("");
  const [selectedId, setSelectedId] = useState<number>();
  const [comparison, setComparison] = useState<{ id: number; text?: string; data?: FixPreview }>();
  const selected = rows.find((r) => r.id === selectedId);
  async function compare(row: InspectedFrame) {
    setComparison({ id: row.id, text: "Translating…" });
    try {
      const res = await fetch("/api/inspect/fix", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ frame: row.orderFrame }),
      });
      if (!res.ok)
        throw Error("This order cannot be represented by the FIX bridge mappings (including explicit STP NONE). No approximate FIX is shown.");
      setComparison({ id: row.id, data: await res.json() as FixPreview });
    } catch (e) {
      setComparison({ id: row.id, text: e instanceof Error ? e.message : "FIX translation unavailable" });
    }
  }
  const preview = comparison?.id === selected?.id ? comparison?.data : undefined;
  const largest = preview ? Math.max(preview.fxp_bytes, preview.fix_bytes, 1) : 1;
  const details = selected && <>
    <h3 className="mb-2 text-sm font-medium">{selected.type} · decoded</h3>
    <pre className={block}>{selected.decoded}</pre>
    <h3 className="mb-2 mt-3 text-sm font-medium">Raw hex</h3>
    <pre className={block}>{selected.hex}</pre>
  </>;
  return (
    <section className="min-h-0 min-w-0 overflow-auto p-4" aria-label="Protocol inspector">
      <div className="rounded border border-neutral-800 bg-neutral-950 p-4">
        <h2 className="font-semibold text-neutral-100">Protocol inspector</h2>
        <p className="mt-1 text-xs text-neutral-400">
          Browser WS application frames · latest 200 · memory only. JWTs and
          token-like fields are redacted before storage. Raw bytes are withheld
          when redaction or unknown fields are present.
        </p>
        <div className="my-4 flex flex-wrap items-end gap-3">
          <label className="grid gap-1 text-xs text-neutral-400">Type
            <select className={control} aria-label="Frame type" value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">All</option>
              {[...new Set(rows.map((r) => r.type))].sort().map((v) => <option key={v}>{v}</option>)}
            </select>
          </label>
          <label className="grid gap-1 text-xs text-neutral-400">Symbol
            <input className={`${control} w-40`} aria-label="Frame symbol" value={symbol} onChange={(e) => setSymbol(e.target.value)} />
          </label>
          <label className="grid gap-1 text-xs text-neutral-400">Direction
            <select className={control} aria-label="Frame direction" value={direction} onChange={(e) => setDirection(e.target.value)}>
              <option value="">Both</option><option>sent</option><option>received</option>
            </select>
          </label>
          <button className={`${neutralButton} text-sm`} onClick={() => {
            useInspector.getState().clear(); setSelectedId(undefined); setComparison(undefined);
          }}>Clear frames</button>
        </div>
        <div className="max-h-52 overflow-auto rounded border border-neutral-800">
          <table className="w-full table-fixed break-words text-left text-xs" data-testid="frame-table">
            <thead className="sticky top-0 bg-neutral-900 text-neutral-400"><tr>
              <th className="w-2/5 p-2 font-medium">UTC timestamp / monotonic ms</th>
              <th className="p-2 font-medium">Direction</th><th className="p-2 font-medium">Type</th>
              <th className="p-2 font-medium">Symbol</th><th className="p-2 font-medium">Bytes</th>
            </tr></thead>
            <tbody>{rows.filter((r) => (!type || r.type === type) && (!symbol || r.symbol.toLowerCase().includes(symbol.toLowerCase())) && (!direction || r.direction === direction)).map((r) => (
              <tr key={r.id} aria-selected={r.id === selectedId} className={`border-t border-neutral-800 ${r.id === selectedId ? "bg-sky-950/60 text-sky-100" : "text-neutral-300 hover:bg-neutral-800/70"}`}>
                <td className="p-2 font-mono">{r.at} / {r.monotonicMs.toFixed(3)}</td>
                <td className="p-2">{r.direction}</td>
                <td className="p-2"><button className={`rounded px-1 py-0.5 underline decoration-neutral-600 underline-offset-2 ${buttonFocus}`} aria-pressed={r.id === selectedId} onClick={() => setSelectedId(r.id)}>{r.type}</button></td>
                <td className="p-2">{r.symbol || "—"}</td><td className="p-2 font-mono">{r.bytes}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
        {!rows.length && <p className="p-3 text-sm text-neutral-400">No frames captured yet. Connect to the local gateway.</p>}
        {selected && (
          <div className="mt-4 min-w-0 space-y-3" data-testid="frame-detail">
            {!preview && <div className="min-w-0">{details}</div>}
            {selected.orderFrame && <button className={`${neutralButton} text-sm`} onClick={() => void compare(selected)}>FXP vs FIX for this order</button>}
            {comparison?.id === selected.id && (
              <div className="min-w-0" role="status">
                {preview ? <>
                  <div className="grid min-w-0 gap-3 min-[900px]:grid-cols-2">
                    {[{ name: "FXP", size: preview.fxp_bytes }, { name: "FIX 4.4", size: preview.fix_bytes }].map(({ name, size }) => (
                      <div key={name} className="min-w-0 rounded border border-neutral-700 bg-neutral-900/50 p-3">
                        <h4 className="font-mono text-lg font-semibold">{name} {size} B</h4>
                        <div className="my-3 h-2 rounded bg-neutral-800" aria-label={`${name} size relative to larger encoding`}>
                          <div className={`h-full rounded ${name === "FXP" ? "bg-sky-500" : "bg-neutral-500"}`} style={{ width: `${size / largest * 100}%` }} />
                        </div>
                        {name === "FXP" ? details : <>
                          <table className="w-full table-fixed break-all text-left font-mono text-[11px] leading-4" aria-label="FIX preview fields">
                            <thead className="text-neutral-500"><tr><th className="w-12 py-1">Tag</th><th className="w-2/5 py-1">Name</th><th className="py-1">Value</th></tr></thead>
                            <tbody>{parseFixPreview(preview.fix).map((field, i) => <tr key={i} className="border-t border-neutral-800"><td className="py-1">{field.tag}</td><td className="py-1 text-neutral-400">{field.name}</td><td className="py-1">{field.value}</td></tr>)}</tbody>
                          </table>
                          <pre className="mt-3 whitespace-pre-wrap break-all border-t border-neutral-700 pt-2 font-mono text-[10px] leading-4 text-neutral-400">{preview.fix}</pre>
                        </>}
                      </div>
                    ))}
                  </div>
                  <p className="mt-3 text-xs leading-5 text-neutral-400">{preview.note}</p>
                </> : <p className="text-sm text-neutral-400">{comparison.text}</p>}
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
