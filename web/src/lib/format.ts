// SPDX-License-Identifier: MIT

/** Formats a scaled integer price (e.g. 15012n at scale 2 -> "150.12") without float loss. */
export function formatPrice(v: bigint | undefined, scale: number): string {
  if (v === undefined) return "—";
  if (scale <= 0) return v.toString();
  const neg = v < 0n;
  const s = (neg ? -v : v).toString().padStart(scale + 1, "0");
  return `${neg ? "-" : ""}${s.slice(0, -scale)}.${s.slice(-scale)}`;
}

export function formatQty(v: bigint | number): string {
  return v.toLocaleString("en-US");
}
