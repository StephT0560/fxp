// SPDX-License-Identifier: MIT
import { neutralButton } from "../lib/styles";
import { TradingPhase } from "../gen/fxp_pb";
import { useSession, useSymbols, type ConnState } from "../lib/stores";

const connColor: Record<ConnState, string> = {
  idle: "bg-neutral-500",
  connecting: "bg-amber-400",
  reconnecting: "bg-amber-400",
  live: "bg-emerald-400",
  rejected: "bg-red-500",
};

export function phaseName(p: TradingPhase | undefined): string {
  switch (p) {
    case TradingPhase.CLOSED:
      return "Closed";
    case TradingPhase.OPENING_CALL:
      return "Opening call";
    case TradingPhase.CONTINUOUS:
      return "Continuous";
    case TradingPhase.CLOSING_CALL:
      return "Closing call";
    default:
      return "Unknown";
  }
}

export function StatusBar({ symbol, onLogout }: { symbol?: string; onLogout: () => void }) {
  const { conn, attempt, error, user, phases } = useSession();
  const symbols = useSymbols((s) => s.list);
  const select = useSymbols((s) => s.select);
  const phase = symbol ? phases[symbol] : undefined;
  return (
    <header className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-neutral-800 px-4 py-2 text-sm">
      <span className="font-semibold">FXP Console</span>
      <span className="flex items-center gap-2" data-testid="conn-state">
        <span className={`inline-block h-2 w-2 rounded-full ${connColor[conn]}`} />
        {conn}
        {conn === "reconnecting" && ` (attempt ${attempt})`}
      </span>
      {error && conn !== "live" && <span className="truncate text-red-400" data-testid="conn-error">{error}</span>}
      <select aria-label="Symbol" data-testid="symbol-select" className="compact-symbol rounded bg-neutral-800 px-2 py-1" value={symbol ?? ""} onChange={(e) => select(e.target.value)}>{symbols.map((s) => <option key={s.symbol}>{s.symbol}</option>)}</select>
      <span data-testid="phase">
        {symbol ?? "—"}: <span className="text-neutral-100">{phaseName(phase)}</span>
      </span>
      <span className="ml-auto text-neutral-400">{user}</span>
      <button onClick={onLogout} className={neutralButton}>
        Log out
      </button>
    </header>
  );
}
