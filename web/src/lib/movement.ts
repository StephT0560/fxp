// SPDX-License-Identifier: MIT
/** Observed prints only: neither a venue session-open nor a daily change. */
export function priceMovement(last?: bigint, previous?: bigint, first?: bigint) {
  return { direction: last === undefined || previous === undefined || last === previous ? 0 : last > previous ? 1 : -1,
    change: last === undefined || first === undefined ? undefined : last - first };
}
