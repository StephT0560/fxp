// SPDX-License-Identifier: MIT
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  MarketDataIncrementalUpdateSchema,
  MarketDataSnapshotSchema,
  MarketOrderUpdateSchema,
  OrderSide,
} from "../gen/fxp_pb";
import { topAsks, topBids } from "./book";
import { flushNow } from "./scheduler";
import { useBook } from "./stores";

/** Deterministic synthetic replay: a random walk of adds/cancels around a mid. */
export function* replay(n: number, seed = 1) {
  let x = seed;
  const rnd = () => ((x = (x * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
  for (let i = 0; i < n; i++) {
    const buy = rnd() < 0.5;
    const off = BigInt(1 + Math.floor(rnd() * 20));
    yield create(MarketDataIncrementalUpdateSchema, {
      symbol: "PERF",
      changes: [
        create(MarketOrderUpdateSchema, {
          updateType: rnd() < 0.6 ? 0 : 2,
          price: buy ? 10_000n - off : 10_000n + off,
          quantity: 1 + Math.floor(rnd() * 100),
          side: buy ? OrderSide.BUY : OrderSide.SELL,
        }),
      ],
    });
  }
}

describe("ladder throughput", () => {
  it("coalesces a burst of incrementals into one store notification", () => {
    useBook.getState().onSnapshot(create(MarketDataSnapshotSchema, { symbol: "PERF" }));
    flushNow();
    let notifications = 0;
    const unsub = useBook.subscribe(() => notifications++);
    for (const u of replay(2000)) useBook.getState().onIncremental(u);
    flushNow();
    unsub();
    expect(notifications).toBe(1);
  });

  // Loose budget: 2,000 updates/s must leave most of each 16 ms frame free.
  it("applies 2,000 updates plus 60 ladder reads well under one second", () => {
    const updates = [...replay(2000, 7)];
    const start = performance.now();
    for (let i = 0; i < updates.length; i++) {
      useBook.getState().onIncremental(updates[i]!);
      if (i % 33 === 0) {
        const b = useBook.getState().books.get("PERF")!;
        topBids(b);
        topAsks(b);
      }
    }
    flushNow();
    const ms = performance.now() - start;
    expect(ms).toBeLessThan(250);
  });
});
