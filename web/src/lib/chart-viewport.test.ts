// SPDX-License-Identifier: MIT
import { expect, it } from "vitest";
import { chartViewport } from "./chart-viewport";
it("fits sparse data and follows at the 20-point boundary", () => {
  expect(chartViewport(0, false)).toBe("keep");
  for (const n of [1, 2, 19]) expect(chartViewport(n, false)).toBe("fit");
  for (const n of [20, 21, 200]) expect(chartViewport(n, false)).toBe("follow");
});
it("preserves manual navigation regardless of point count", () => {
  for (const n of [0, 1, 19, 20, 200]) expect(chartViewport(n, true)).toBe("keep");
});
