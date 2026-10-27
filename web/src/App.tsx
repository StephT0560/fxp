// SPDX-License-Identifier: MIT
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { AuctionBanner } from "./components/AuctionBanner";
import { Blotter } from "./components/Blotter";
import { Chart } from "./components/Chart";
import { Ladder } from "./components/Ladder";
import { Login } from "./components/Login";
import { OrderTicket } from "./components/OrderTicket";
import { SimControls } from "./components/SimControls";
import { StatusBar } from "./components/StatusBar";
import { SymbolList } from "./components/SymbolList";
import { TapeView } from "./components/TapeView";
import { useAuctionTimers } from "./lib/auction-clock";
import { buttonFocus } from "./lib/styles";
import { activeClient, ConsoleClient } from "./lib/client";
import { formatPrice, formatQty } from "./lib/format";
import {
  useOrders,
  useSession,
  useSymbols,
  useTape,
  type SymbolInfo,
} from "./lib/stores";

const ScenariosPage = lazy(() => import("./components/ScenariosPage"));
const LatencyPanel = lazy(() => import("./components/LatencyPanel"));
import { ProtocolInspector } from "./components/ProtocolInspector";
import { useInspector } from "./lib/inspector";

function Stats({ symbol, scale }: { symbol: string; scale: number }) {
  useTape((s) => s.version);
  const t = useTape.getState().tapes.get(symbol);
  return (
    <div className="flex gap-6 text-sm">
      <span>
        last{" "}
        <span className="num text-neutral-100" data-testid="last-price">
          {formatPrice(t?.lastPrice, scale)}
        </span>
      </span>
      <span>
        <span title="Volume of prints observed since this console connected">
          observed vol
        </span>{" "}
        <span className="num text-neutral-100" data-testid="volume">
          {formatQty(t?.volume ?? 0n)}
        </span>
      </span>
    </div>
  );
}

export function App() {
  const user = useSession((s) => s.user);
  useAuctionTimers(Boolean(user));
  const setUser = useSession((s) => s.setUser);
  const { list, selected, setList } = useSymbols();
  const client = useRef<ConsoleClient>(undefined);
  const [panel, setPanel] = useState("market");
  const [error, setError] = useState<string>();

  useEffect(() => {
    fetch("/api/symbols")
      .then((r) =>
        r.ok
          ? (r.json() as Promise<SymbolInfo[]>)
          : Promise.reject(new Error(`symbols: ${r.status}`)),
      )
      .then(setList)
      .catch((e: Error) => setError(e.message));
  }, [setList]);

  useEffect(() => {
    if (!user) return;
    useOrders.getState().reset();
    useInspector.getState().clear();
    const c = new ConsoleClient({
      user,
      symbols: () => useSymbols.getState().list.map((s) => s.symbol),
    });
    client.current = c;
    activeClient.current = c;
    c.start();
    return () => {
      c.stop();
      useInspector.getState().clear();
      if (activeClient.current === c) activeClient.current = undefined;
    };
  }, [user]);

  if (!user) return <Login onLogin={setUser} />;
  const info = list.find((s) => s.symbol === selected);
  const scale = info?.price_scale ?? 2;
  return (
    <div className="flex h-full flex-col">
      <StatusBar symbol={selected} onLogout={() => setUser(undefined)} />
      <nav
        className="flex gap-1 border-b border-neutral-800 bg-neutral-950 px-4 py-2"
        aria-label="Console panels"
      >
        {["market", "scenarios", "latency", "protocol"].map((v) => (
          <button
            key={v}
            className={`rounded px-3 py-1 text-sm text-neutral-400 hover:bg-neutral-800 aria-pressed:bg-neutral-700 aria-pressed:text-white ${buttonFocus}`}
            aria-pressed={panel === v}
            onClick={() => setPanel(v)}
          >
            {v === "market"
              ? "Market"
              : v === "scenarios"
                ? "Scenarios"
                : v === "latency"
                  ? "Latency"
                  : "Protocol inspector"}
          </button>
        ))}
      </nav>
      {panel === "scenarios" && (
        <Suspense fallback={<p>Loading scenarios…</p>}>
          <ScenariosPage />
        </Suspense>
      )}
      {panel === "latency" && (
        <Suspense fallback={<p>Loading latency panel…</p>}>
          <LatencyPanel />
        </Suspense>
      )}
      {panel === "protocol" && <ProtocolInspector />}
      <div className={panel === "market" ? "contents" : "hidden"}>
        {selected && <AuctionBanner symbol={selected} scale={scale} />}
        {error && <div className="px-4 py-1 text-sm text-red-400">{error}</div>}
        <div className="flex min-h-0 flex-1">
          <SymbolList />
          {selected && (
            <>
              <Ladder symbol={selected} scale={scale} />
              <main className="flex min-w-0 flex-1 flex-col gap-2 p-3">
                <div className="flex items-center justify-between">
                  <Stats symbol={selected} scale={scale} />
                  <SimControls />
                </div>
                <Chart symbol={selected} scale={scale} />
              </main>
              <TapeView symbol={selected} scale={scale} />
            </>
          )}
        </div>
        {info && (
          <div className="flex h-72 shrink-0 border-t border-neutral-800">
            <OrderTicket key={selected} rules={info} />
            <Blotter symbol={selected!} />
          </div>
        )}
      </div>
    </div>
  );
}
