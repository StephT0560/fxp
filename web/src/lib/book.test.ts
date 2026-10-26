// SPDX-License-Identifier: MIT
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  MarketDataIncrementalUpdateSchema,
  MarketDataSnapshotSchema,
  MarketOrderBookEntrySchema,
  MarketOrderUpdateSchema,
  OrderSide,
} from "../gen/fxp_pb";
import { applyIncremental, applySnapshot, emptyBook, isCrossed, topAsks, topBids } from "./book";

const lvl = (price: bigint, size: number, side: OrderSide) => create(MarketOrderBookEntrySchema, { price, size, side });
const snap = () =>
  create(MarketDataSnapshotSchema, {
    symbol: "AAPL",
    bidLevels: [lvl(100n, 5, OrderSide.BUY), lvl(99n, 7, OrderSide.BUY)],
    askLevels: [lvl(101n, 3, OrderSide.SELL), lvl(102n, 4, OrderSide.SELL)],
  });
const inc = (...changes: [number, bigint, number, OrderSide][]) =>
  create(MarketDataIncrementalUpdateSchema, {
    symbol: "AAPL",
    changes: changes.map(([updateType, price, quantity, side]) =>
      create(MarketOrderUpdateSchema, { updateType, price, quantity, side }),
    ),
  });

describe("book", () => {
  it("drops incrementals until a snapshot arrives", () => {
    const b = emptyBook("AAPL");
    expect(applyIncremental(b, inc([0, 100n, 5, OrderSide.BUY]))).toBe(false);
    expect(b.bids.size).toBe(0);
  });

  it("applies snapshot then order-level deltas", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, snap());
    applyIncremental(b, inc([0, 100n, 2, OrderSide.BUY], [1, 101n, 1, OrderSide.SELL], [2, 102n, 4, OrderSide.SELL]));
    expect(topBids(b)).toEqual([
      { price: 100n, qty: 7 },
      { price: 99n, qty: 7 },
    ]);
    expect(topAsks(b)).toEqual([{ price: 101n, qty: 2 }]);
    expect(b.anomalies).toBe(0);
  });

  it("orders bids descending and asks ascending, capped at depth", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, create(MarketDataSnapshotSchema, { symbol: "AAPL" }));
    for (let i = 0; i < 15; i++) applyIncremental(b, inc([0, BigInt(100 - i), 1, OrderSide.BUY], [0, BigInt(200 + i), 1, OrderSide.SELL]));
    expect(topBids(b).map((l) => l.price)).toEqual(Array.from({ length: 10 }, (_, i) => BigInt(100 - i)));
    expect(topAsks(b).map((l) => l.price)).toEqual(Array.from({ length: 10 }, (_, i) => BigInt(200 + i)));
  });

  it("survives out-of-order cancel before add without negative levels", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, snap());
    applyIncremental(b, inc([2, 98n, 3, OrderSide.BUY]));
    expect(b.bids.has(98n)).toBe(false);
    expect(b.anomalies).toBe(1);
    applyIncremental(b, inc([0, 98n, 3, OrderSide.BUY]));
    expect(b.bids.get(98n)).toBe(3);
  });

  it("flags unknown side / update type as anomalies", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, snap());
    applyIncremental(b, inc([0, 100n, 1, OrderSide.ORDER_SIDE_UNSPECIFIED], [7, 100n, 1, OrderSide.BUY]));
    expect(b.anomalies).toBe(2);
    expect(b.bids.get(100n)).toBe(5);
  });

  it("detects a crossed book instead of hiding it", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, snap());
    expect(isCrossed(topBids(b), topAsks(b))).toBe(false);
    applyIncremental(b, inc([0, 101n, 1, OrderSide.BUY]));
    expect(isCrossed(topBids(b), topAsks(b))).toBe(true);
  });

  it("snapshot replaces previous state", () => {
    const b = emptyBook("AAPL");
    applySnapshot(b, snap());
    applyIncremental(b, inc([0, 50n, 1, OrderSide.BUY]));
    applySnapshot(b, snap());
    expect(b.bids.has(50n)).toBe(false);
  });

  it("keeps int64 prices beyond 2^53 exact", () => {
    const big = 2n ** 62n + 1n;
    const b = emptyBook("AAPL");
    applySnapshot(b, create(MarketDataSnapshotSchema, { symbol: "AAPL", bidLevels: [lvl(big, 1, OrderSide.BUY)] }));
    expect(topBids(b)[0]?.price).toBe(big);
  });
});
