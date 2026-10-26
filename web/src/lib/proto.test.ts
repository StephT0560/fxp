// SPDX-License-Identifier: MIT
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { OrderSide, TradingPhase } from "../gen/fxp_pb";
import { decodeFrame, logonFrame, subscribeFrame } from "./proto";

// Fixtures are encoded independently by go_gateway/cmd/console_golden.
const golden = (name: string) =>
  decodeFrame(readFileSync(resolve(__dirname, `../testdata/golden/${name}.bin`)));
const ts = { seconds: 1_700_000_000n, nanos: 123_000_000 };

describe("golden frames from the Go encoder", () => {
  it("decodes TradingSessionStatus", () => {
    const m = golden("session_status");
    expect(m.payload.case).toBe("tradingSessionStatus");
    if (m.payload.case !== "tradingSessionStatus") return;
    expect(m.payload.value.symbol).toBe("AAPL");
    expect(m.payload.value.phase).toBe(TradingPhase.OPENING_CALL);
    expect(m.payload.value.timestamp).toMatchObject(ts);
  });

  it("decodes MarketDataSnapshot", () => {
    const m = golden("snapshot");
    if (m.payload.case !== "marketDataSnapshot") throw new Error(m.payload.case);
    const s = m.payload.value;
    expect(s.header?.sequenceNumber).toBe(7n);
    expect(s.bidLevels.map((l) => [l.price, l.size, l.side])).toEqual([
      [15000n, 300, OrderSide.BUY],
      [14999n, 100, OrderSide.BUY],
    ]);
    expect(s.askLevels.map((l) => [l.price, l.size, l.side])).toEqual([[15002n, 200, OrderSide.SELL]]);
    expect(s.bidAskSpread).toBe(2n);
    expect(s.totalVolume).toBe(600n);
  });

  it("decodes MarketDataIncrementalUpdate", () => {
    const m = golden("incremental");
    if (m.payload.case !== "marketDataIncremental") throw new Error(m.payload.case);
    expect(m.payload.value.changes.map((c) => [c.updateType, c.price, c.quantity, c.side])).toEqual([
      [0, 15001n, 50, OrderSide.BUY],
      [1, 15002n, 20, OrderSide.SELL],
      [2, 14999n, 100, OrderSide.BUY],
    ]);
  });

  it("decodes TradeCaptureReport with exact 64-bit values", () => {
    const m = golden("trade");
    if (m.payload.case !== "tradeCaptureReport") throw new Error(m.payload.case);
    const t = m.payload.value;
    expect(t.symbol).toBe("BTC/USD");
    expect(t.tradeId).toBe("T-42");
    expect(t.tradePrice).toBe(9_007_199_254_740_993n);
    expect(t.seq).toBe(18_446_744_073_709_551_615n);
    expect(t.tradeQuantity).toBe(3);
    expect(t.aggressorSide).toBe(OrderSide.SELL);
    expect(t.isAuction).toBe(false);
    expect(t.buyOrderId).toBe("");
  });

  it("decodes AuctionInfo", () => {
    const m = golden("auction_info");
    if (m.payload.case !== "auctionInfo") throw new Error(m.payload.case);
    expect(m.payload.value).toMatchObject({
      symbol: "AAPL",
      phase: TradingPhase.CLOSING_CALL,
      indicativePrice: 15001n,
      indicativeVolume: 250n,
      imbalanceQuantity: 40n,
      imbalanceSide: OrderSide.BUY,
      referencePrice: 15000n,
    });
  });
});

describe("outbound frames", () => {
  it("logon round-trips", () => {
    const m = decodeFrame(logonFrame("jwt", "s1"));
    if (m.payload.case !== "logon") throw new Error(m.payload.case);
    expect(m.payload.value.token).toBe("jwt");
    expect(m.payload.value.sessionId).toBe("s1");
  });

  it("market data request uses oneof field 11", () => {
    const b = subscribeFrame(["AAPL"]);
    expect(b[0]).toBe((11 << 3) | 2);
    const m = decodeFrame(b);
    if (m.payload.case !== "marketDataRequest") throw new Error(m.payload.case);
    expect(m.payload.value.symbols).toEqual(["AAPL"]);
  });
});
