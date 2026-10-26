// SPDX-License-Identifier: MIT

/**
 * Coalesces store notifications to one per animation frame so a burst of
 * incrementals costs one React render, not one per message.
 */
const pending = new Set<() => void>();
let scheduled = false;

const raf: (cb: () => void) => void =
  typeof requestAnimationFrame === "function" ? (cb) => requestAnimationFrame(cb) : (cb) => setTimeout(cb, 16);

export function flushNow(): void {
  scheduled = false;
  const fns = [...pending];
  pending.clear();
  for (const fn of fns) fn();
}

export function schedule(fn: () => void): void {
  pending.add(fn);
  if (!scheduled) {
    scheduled = true;
    raf(flushNow);
  }
}
