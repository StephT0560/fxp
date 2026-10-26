// SPDX-License-Identifier: MIT
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { TradeCaptureReportSchema } from "../gen/fxp_pb";
import { formatPrice } from "./format";
import { applyTrade, emptyTape, TAPE_LIMIT } from "./tape";

const tr = (seq: bigint, price = 100n, qty = 1) =>
  create(TradeCaptureReportSchema, { symbol: "AAPL", seq, tradePrice: price, tradeQuantity: qty });

describe("tape", () => {
  it("tracks last price and volume", () => {
    const t = emptyTape();
    applyTrade(t, tr(1n, 100n, 2));
    applyTrade(t, tr(2n, 101n, 3));
    expect(t.lastPrice).toBe(101n);
    expect(t.volume).toBe(5n);
    expect(t.gapCount).toBe(0);
  });

  it("joining mid-session is not a gap", () => {
    const t = emptyTape();
    applyTrade(t, tr(40n));
    expect(t.gapCount).toBe(0);
    expect(t.expected).toBe(41n);
  });

  it("records each sequence gap and the missed count", () => {
    const t = emptyTape();
    applyTrade(t, tr(1n));
    applyTrade(t, tr(4n));
    applyTrade(t, tr(5n));
    applyTrade(t, tr(9n));
    expect(t.gapCount).toBe(2);
    expect(t.missed).toBe(5n);
    expect(t.lastGap).toEqual({ from: 6n, to: 8n });
  });

  it("drops duplicate and late prints without double counting", () => {
    const t = emptyTape();
    applyTrade(t, tr(1n, 100n, 2));
    applyTrade(t, tr(2n, 100n, 2));
    expect(applyTrade(t, tr(2n, 100n, 2))).toBe(false);
    expect(applyTrade(t, tr(1n, 100n, 2))).toBe(false);
    expect(t.volume).toBe(4n);
    expect(t.stale).toBe(2);
    expect(t.gapCount).toBe(0);
  });

  it("bounds the tape", () => {
    const t = emptyTape();
    for (let i = 1; i <= TAPE_LIMIT + 50; i++) applyTrade(t, tr(BigInt(i)));
    expect(t.prints.length).toBe(TAPE_LIMIT);
    expect(t.prints[0]?.seq).toBe(BigInt(TAPE_LIMIT + 50));
  });
});

describe("formatPrice", () => {
  it("formats scaled integers exactly", () => {
    expect(formatPrice(15012n, 2)).toBe("150.12");
    expect(formatPrice(5n, 2)).toBe("0.05");
    expect(formatPrice(-5n, 2)).toBe("-0.05");
    expect(formatPrice(2n ** 63n - 1n, 2)).toBe("92233720368547758.07");
    expect(formatPrice(undefined, 2)).toBe("—");
  });
});
