// SPDX-License-Identifier: MIT
export function chartViewport(points: number, userAdjusted: boolean): "keep" | "fit" | "follow" {
  if (userAdjusted || points === 0) return "keep";
  return points < 20 ? "fit" : "follow";
}
