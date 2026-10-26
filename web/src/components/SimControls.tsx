// SPDX-License-Identifier: MIT
import { useState } from "react";

export function SimControls() {
  const [seed, setSeed] = useState(42);
  const [duration, setDuration] = useState(120);
  const [auction, setAuction] = useState(true);
  const [auctionSec, setAuctionSec] = useState(20);
  const [msg, setMsg] = useState<string>();
  const call = async (path: string, body?: unknown) => {
    setMsg(undefined);
    const res = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    setMsg(
      res.ok
        ? path.endsWith("start")
          ? auction
            ? `simulator running · opening auction for ${auctionSec}s`
            : "simulator running"
          : "simulator stopped"
        : (await res.text()).trim(),
    );
  };
  const num = "num rounded bg-neutral-800 px-1";
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <label className="text-neutral-400">
        seed <input data-testid="sim-seed" type="number" value={seed} onChange={(e) => setSeed(Number(e.target.value))} className={`${num} w-20`} />
      </label>
      <label className="text-neutral-400">
        secs{" "}
        <input data-testid="sim-duration" type="number" min={1} max={300} value={duration} onChange={(e) => setDuration(Number(e.target.value))} className={`${num} w-16`} />
      </label>
      <label className="flex items-center gap-1 text-neutral-400" title="Puts every symbol into an opening call, then uncrosses it">
        <input data-testid="sim-auction" type="checkbox" checked={auction} onChange={(e) => setAuction(e.target.checked)} />
        open with auction
        <input
          data-testid="sim-auction-secs"
          type="number"
          min={5}
          max={120}
          disabled={!auction}
          value={auctionSec}
          onChange={(e) => setAuctionSec(Number(e.target.value))}
          className={`${num} w-14 disabled:opacity-40`}
        />
        s
      </label>
      <button
        data-testid="sim-start"
        onClick={() =>
          void call("/api/sim/start", { seed, symbols: 10, participants: 20, duration, ...(auction ? { opening_auction_sec: auctionSec } : {}) })
        }
        className="rounded bg-blue-700 px-2 py-1 hover:bg-blue-600"
      >
        Start simulator
      </button>
      <button data-testid="sim-stop" onClick={() => void call("/api/sim/stop")} className="rounded bg-neutral-800 px-2 py-1 hover:bg-neutral-700">
        Stop
      </button>
      {msg && (
        <span className="text-neutral-400" data-testid="sim-msg">
          {msg}
        </span>
      )}
    </div>
  );
}
