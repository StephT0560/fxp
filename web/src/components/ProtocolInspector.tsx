// SPDX-License-Identifier: MIT
import { useState } from "react";
import { useInspector, type InspectedFrame } from "../lib/inspector";

export function ProtocolInspector() {
  useInspector((s) => s.version);
  const rows = useInspector.getState().rows;
  const [type, setType] = useState("");
  const [symbol, setSymbol] = useState("");
  const [direction, setDirection] = useState("");
  const [selectedId, setSelectedId] = useState<number>();
  const [comparison, setComparison] = useState<{ id: number; text: string }>();
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
        throw Error(
          "This order cannot be represented by the FIX bridge mappings (including explicit STP NONE). No approximate FIX is shown.",
        );
      const d = (await res.json()) as {
        fix: string;
        fix_bytes: number;
        fxp_bytes: number;
        note: string;
      };
      setComparison({
        id: row.id,
        text: `FXP ${d.fxp_bytes} bytes · FIX 4.4 ${d.fix_bytes} bytes\n${d.fix}\n${d.note}`,
      });
    } catch (e) {
      setComparison({
        id: row.id,
        text: e instanceof Error ? e.message : "FIX translation unavailable",
      });
    }
  }
  return (
    <section className="p-4 overflow-auto" aria-label="Protocol inspector">
      <h2>Protocol inspector</h2>
      <p>
        Browser WS application frames · latest 200 · memory only. JWTs and
        token-like fields are redacted before storage. Raw bytes are withheld
        when redaction or unknown fields are present.
      </p>
      <div className="flex gap-3 my-3">
        <label>
          Type{" "}
          <select
            aria-label="Frame type"
            value={type}
            onChange={(e) => setType(e.target.value)}
          >
            <option value="">All</option>
            {[...new Set(rows.map((r) => r.type))].sort().map((v) => (
              <option key={v}>{v}</option>
            ))}
          </select>
        </label>
        <label>
          Symbol{" "}
          <input
            aria-label="Frame symbol"
            value={symbol}
            onChange={(e) => setSymbol(e.target.value)}
          />
        </label>
        <label>
          Direction{" "}
          <select
            aria-label="Frame direction"
            value={direction}
            onChange={(e) => setDirection(e.target.value)}
          >
            <option value="">Both</option>
            <option>sent</option>
            <option>received</option>
          </select>
        </label>
        <button
          onClick={() => {
            useInspector.getState().clear();
            setSelectedId(undefined);
            setComparison(undefined);
          }}
        >
          Clear frames
        </button>
      </div>
      <table className="w-full text-left" data-testid="frame-table">
        <thead>
          <tr>
            <th>UTC timestamp / monotonic ms</th>
            <th>Direction</th>
            <th>Type</th>
            <th>Symbol</th>
            <th>Bytes</th>
          </tr>
        </thead>
        <tbody>
          {rows
            .filter(
              (r) =>
                (!type || r.type === type) &&
                (!symbol ||
                  r.symbol.toLowerCase().includes(symbol.toLowerCase())) &&
                (!direction || r.direction === direction),
            )
            .map((r) => (
              <tr key={r.id}>
                <td>
                  {r.at} / {r.monotonicMs.toFixed(3)}
                </td>
                <td>{r.direction}</td>
                <td>
                  <button onClick={() => setSelectedId(r.id)}>{r.type}</button>
                </td>
                <td>{r.symbol || "—"}</td>
                <td>{r.bytes}</td>
              </tr>
            ))}
        </tbody>
      </table>
      {!rows.length && (
        <p>No frames captured yet. Connect to the local gateway.</p>
      )}
      {selected && (
        <div className="mt-3" data-testid="frame-detail">
          <h3>{selected.type} · decoded</h3>
          <pre className="whitespace-pre-wrap break-all">
            {selected.decoded}
          </pre>
          <h3>Raw hex</h3>
          <pre className="whitespace-pre-wrap break-all">{selected.hex}</pre>
          {selected.orderFrame && (
            <button onClick={() => void compare(selected)}>
              FXP vs FIX for this order
            </button>
          )}
          {comparison?.id === selected.id && (
            <pre className="whitespace-pre-wrap break-all" role="status">
              {comparison.text}
            </pre>
          )}
        </div>
      )}
    </section>
  );
}
