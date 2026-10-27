// SPDX-License-Identifier: MIT
import { useEffect, useRef } from "react";
import { create } from "zustand";
import { auctionDeadline, type SimStatus } from "./auction";
import { useSession } from "./stores";
export const useAuctionClock = create<{ deadlines: Record<string, number>; now: number; ownedStartedAt?: string }>(() => ({ deadlines: {}, now: Date.now() }));
export function confirmSimulatorStart(status: SimStatus): void { useAuctionClock.setState({ ownedStartedAt: status.running ? status.started_at : undefined }); }
/** Mounted once by App: one clock for every banner, including observer panels. */
export function useAuctionTimers(enabled: boolean): void {
  const calls = useSession((s) => s.openingCalls);
  const deadlines = useAuctionClock((s) => s.deadlines);
  const ownedStartedAt = useAuctionClock((s) => s.ownedStartedAt);
  const previousCalls = useRef(calls);
  useEffect(() => {
    const retained: Record<string, number> = {};
    for (const [symbol, call] of Object.entries(calls)) {
      const deadline = useAuctionClock.getState().deadlines[symbol];
      if (enabled && previousCalls.current[symbol]?.at === call.at && deadline !== undefined) retained[symbol] = deadline;
    }
    previousCalls.current = calls;
    useAuctionClock.setState({ deadlines: retained, ...(!enabled ? { ownedStartedAt: undefined } : {}) });
    if (!enabled || !Object.keys(calls).some((symbol) => retained[symbol] === undefined)) return;
    const c = new AbortController();
    fetch("/api/sim/status", { signal: c.signal }).then((r) => { if (!r.ok) throw Error(); return r.json() as Promise<SimStatus>; })
      .then((status) => {
        if (c.signal.aborted) return;
        const deadlines: Record<string, number> = { ...retained };
        for (const [symbol, call] of Object.entries(calls)) {
          if (deadlines[symbol] !== undefined) continue;
          const deadline = auctionDeadline(call, status, ownedStartedAt);
          if (deadline !== undefined) deadlines[symbol] = deadline;
        }
        useAuctionClock.setState({ deadlines, now: Date.now() });
      }).catch(() => { /* Unknown source/duration: never guess. */ });
    return () => c.abort();
  }, [enabled, calls, ownedStartedAt]);
  useEffect(() => {
    if (!enabled || !Object.keys(deadlines).length) return;
    const timer = setInterval(() => useAuctionClock.setState({ now: Date.now() }), 1000);
    return () => clearInterval(timer);
  }, [enabled, deadlines]);
}
