// SPDX-License-Identifier: MIT
import type { TradeCaptureReport } from "../gen/fxp_pb";

export const TAPE_LIMIT = 200;

export interface Print {
  key: string;
  seq: bigint;
  price: bigint;
  qty: number;
  aggressor: number;
  auction: boolean;
  timeMs: number;
}

export interface Gap {
  from: bigint;
  to: bigint;
}

export interface Tape {
  prints: Print[];
  /** Next expected per-symbol seq; 0n until the first print. */
  expected: bigint;
  lastPrice?: bigint;
  /** Volume of prints observed since this console joined, not the session total. */
  volume: bigint;
  gapCount: number;
  missed: bigint;
  lastGap?: Gap;
  stale: number;
}

export function emptyTape(): Tape {
  return { prints: [], expected: 0n, volume: 0n, gapCount: 0, missed: 0n, stale: 0 };
}

export function timeMs(t: { seconds: bigint; nanos: number } | undefined): number {
  return t ? Number(t.seconds) * 1000 + Math.floor(t.nanos / 1e6) : Date.now();
}

/**
 * Forgets the sequence baseline after a fresh subscription (snapshot) so a
 * reset or resumed stream is neither dropped as stale nor reported as a gap.
 */
export function resyncTape(tape: Tape): void {
  tape.expected = 0n;
}

/**
 * Applies one public print. A seq above the expected one records a gap
 * (best-effort MD loss); a seq below it is a duplicate/out-of-order print
 * and is dropped so volume is never double counted.
 */
export function applyTrade(tape: Tape, tr: TradeCaptureReport): boolean {
  if (tape.expected !== 0n && tr.seq < tape.expected) {
    tape.stale++;
    return false;
  }
  if (tape.expected !== 0n && tr.seq > tape.expected) {
    tape.gapCount++;
    tape.missed += tr.seq - tape.expected;
    tape.lastGap = { from: tape.expected, to: tr.seq - 1n };
  }
  tape.expected = tr.seq + 1n;
  tape.lastPrice = tr.tradePrice;
  tape.volume += BigInt(tr.tradeQuantity);
  tape.prints.unshift({
    key: `${tr.symbol}:${tr.seq}`,
    seq: tr.seq,
    price: tr.tradePrice,
    qty: tr.tradeQuantity,
    aggressor: tr.aggressorSide,
    auction: tr.isAuction,
    timeMs: timeMs(tr.timestamp),
  });
  if (tape.prints.length > TAPE_LIMIT) tape.prints.length = TAPE_LIMIT;
  return true;
}
