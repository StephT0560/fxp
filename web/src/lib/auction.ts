// SPDX-License-Identifier: MIT
export interface CallObservation { at: number; knownTransition: boolean }
export interface SimStatus { running: boolean; started_at?: string; params?: { opening_auction_sec?: number } }
export function countdownSeconds(deadline: number, now: number): number {
  return Math.max(0, Math.ceil((deadline - now) / 1000));
}
/** An observed-arrival estimate, only when the simulator is the known source. */
export function auctionDeadline(observation: CallObservation, status: SimStatus, ownedStartedAt?: string): number | undefined {
  const seconds = status.params?.opening_auction_sec;
  const started = Date.parse(status.started_at ?? "");
  if (!status.running || !Number.isInteger(seconds) || seconds! < 5 || seconds! > 120 || !Number.isFinite(started) || started > observation.at) return;
  // A first snapshot may join an existing call. Only our fresh start request
  // makes that case attributable; otherwise require a previously known phase.
  if (!observation.knownTransition && !(ownedStartedAt !== undefined && status.started_at === ownedStartedAt)) return;
  return observation.at + seconds! * 1000;
}
