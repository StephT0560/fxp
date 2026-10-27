// SPDX-License-Identifier: MIT
import { expect, it } from "vitest";
import { priceMovement } from "./movement";
it("uses the previous print for direction and the first observed print for change", () => {
  expect(priceMovement(105n, 110n, 100n)).toEqual({ direction: -1, change: 5n });
  expect(priceMovement(105n, 101n, 110n)).toEqual({ direction: 1, change: -5n });
  expect(priceMovement(105n, 105n, 100n).direction).toBe(0);
});
it("keeps large prices exact and leaves missing observations unknown", () => {
  const first = 9007199254740993n;
  expect(priceMovement(first + 2n, first + 1n, first)).toEqual({ direction: 1, change: 2n });
  expect(priceMovement()).toEqual({ direction: 0, change: undefined });
  expect(priceMovement(first, undefined, first)).toEqual({ direction: 0, change: 0n });
});
