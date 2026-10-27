// SPDX-License-Identifier: MIT
import { expect, it } from "vitest";
import { OrderSide } from "../gen/fxp_pb";
import type { OrderRow } from "./orders";
import { ownLevels } from "./own-levels";
const row = (p: Partial<OrderRow> = {}): OrderRow => ({ orderId: "a", symbol: "DEMO", side: OrderSide.BUY, price: 9007199254740993n, qty: 10, leaves: 10, cum: 0, displayQty: 0, avgPx: 0n, state: "new", restated: false, updated: 0, ...p });
const level = { price: 9007199254740993n, qty: 100 };
it("sums remaining quantity at exact bigint prices and keeps sides separate", () => {
  const v = ownLevels([row(), row({ state: "partial", cum: 4, leaves: 6 }), row({ side: OrderSide.SELL, leaves: 3 }), row({ price: level.price + 1n, leaves: 2 })], "DEMO", [level], [level]);
  expect(v.buy.get(level.price)).toBe(16); expect(v.sell.get(level.price)).toBe(3);
  expect(v.buy.get(level.price + 1n)).toBe(2); expect(v.offLadder).toBe(1);
});
it("removes canceled/replaced originals and uses the replacement's known price", () => {
  const v = ownLevels([row({ state: "replaced" }), row({ state: "canceled" }), row({ orderId: "replacement", price: level.price + 1n, leaves: 4 }), row({ symbol: "OTHER" })], "DEMO", [level], []);
  expect([...v.buy]).toEqual([[level.price + 1n, 4]]); expect(v.offLadder).toBe(1);
});
it("counts unknown prices honestly and off-ladder orders rather than price levels", () => {
  const v = ownLevels([row({ price: undefined }), row({ price: undefined, state: "filled", leaves: 0 }), row(), row(), row({ leaves: 0 })], "DEMO", [], []);
  expect(v.unknown).toBe(1); expect(v.offLadder).toBe(2);
});
