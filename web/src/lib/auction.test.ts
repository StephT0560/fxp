// SPDX-License-Identifier: MIT
import { useSession } from "./stores";
import { TradingPhase } from "../gen/fxp_pb";
import { expect, it, vi } from "vitest";
import { auctionDeadline, countdownSeconds } from "./auction";
it("counts seconds once per tick, rounds up and clamps to uncrossing", () => {
  expect(countdownSeconds(12000, 0)).toBe(12); expect(countdownSeconds(12000, 1001)).toBe(11);
  expect(countdownSeconds(12000, 12000)).toBe(0); expect(countdownSeconds(12000, 99999)).toBe(0);
});
it("requires a known simulator duration and refuses unknown late joins", () => {
  const sim = { running: true, started_at: new Date(1000).toISOString(), params: { opening_auction_sec: 10 } };
  expect(auctionDeadline({ at: 2000, knownTransition: true }, sim)).toBe(12000);
  expect(auctionDeadline({ at: 2000, knownTransition: false }, sim)).toBeUndefined();
  expect(auctionDeadline({ at: 2000, knownTransition: false }, sim, sim.started_at)).toBe(12000);
  expect(auctionDeadline({ at: 2000, knownTransition: false }, sim, new Date(1500).toISOString())).toBeUndefined();
  expect(auctionDeadline({ at: 2000, knownTransition: true }, { ...sim, running: false })).toBeUndefined();
  expect(auctionDeadline({ at: 2000, knownTransition: true }, { ...sim, params: {} })).toBeUndefined();
});

it("duplicate status observations do not restart the call's arrival clock", () => {
  const clock = vi.spyOn(Date, "now").mockReturnValue(1000);
  try {
    useSession.setState({ phases: {}, openingCalls: {} });
    useSession.getState().onStatus("AAPL", TradingPhase.CONTINUOUS);
    useSession.getState().onStatus("AAPL", TradingPhase.OPENING_CALL);
    clock.mockReturnValue(4000);
    useSession.getState().onStatus("AAPL", TradingPhase.OPENING_CALL);
    expect(useSession.getState().openingCalls.AAPL).toEqual({ at: 1000, knownTransition: true });
    useSession.getState().onStatus("AAPL", TradingPhase.CONTINUOUS);
    expect(useSession.getState().openingCalls.AAPL).toBeUndefined();
  } finally { clock.mockRestore(); }
});
