// SPDX-License-Identifier: MIT
import { OrderSide, TradingPhase } from "../gen/fxp_pb";
import { formatPrice, formatQty } from "../lib/format";
import { useSession } from "../lib/stores";
import { phaseName } from "./StatusBar";

export function AuctionBanner({ symbol, scale }: { symbol: string; scale: number }) {
  const phase = useSession((s) => s.phases[symbol]);
  const a = useSession((s) => s.auctions[symbol]);
  const inCall = phase === TradingPhase.OPENING_CALL || phase === TradingPhase.CLOSING_CALL;
  if (!inCall || !a) return null;
  const imb =
    a.imbalanceSide === OrderSide.BUY ? "buy" : a.imbalanceSide === OrderSide.SELL ? "sell" : "balanced";
  return (
    <div data-testid="auction-banner" className="border-b border-amber-700 bg-amber-950 px-4 py-1 text-sm text-amber-200">
      {phaseName(phase)} auction · indicative{" "}
      <span className="num">{a.indicativeVolume > 0n ? formatPrice(a.indicativePrice, scale) : "—"}</span> × {" "}
      <span className="num">{formatQty(a.indicativeVolume)}</span> · imbalance{" "}
      <span className="num">{formatQty(a.imbalanceQuantity)}</span> {imb}
    </div>
  );
}
