// SPDX-License-Identifier: MIT
import { useEffect, useState } from "react";
import { OrderSide, OrderType, SelfTradePrevention, TimeInForce } from "../gen/fxp_pb";
import { activeClient } from "../lib/client";
import { useOrders, useSession } from "../lib/stores";
import { needsPrice, needsStop, todayUtc, validateTicket, type Field, type SymbolRules, type TicketInput } from "../lib/validate";

const TYPES: [OrderType, string][] = [
  [OrderType.LIMIT, "Limit"],
  [OrderType.MARKET, "Market"],
  [OrderType.STOP, "Stop"],
  [OrderType.STOP_LIMIT, "Stop limit"],
];
const TIFS: [TimeInForce, string][] = [
  [TimeInForce.GTC, "GTC"],
  [TimeInForce.DAY, "Day"],
  [TimeInForce.IOC, "IOC"],
  [TimeInForce.FOK, "FOK"],
  [TimeInForce.GTD, "GTD"],
  [TimeInForce.AT_OPEN, "At open"],
];
const STPS: [SelfTradePrevention, string][] = [
  [SelfTradePrevention.UNSPECIFIED, "default"],
  [SelfTradePrevention.NONE, "none"],
  [SelfTradePrevention.CANCEL_NEWEST, "cancel newest"],
  [SelfTradePrevention.CANCEL_OLDEST, "cancel oldest"],
  [SelfTradePrevention.CANCEL_BOTH, "cancel both"],
  [SelfTradePrevention.DECREMENT_AND_CANCEL, "decrement and cancel"],
];
const SUBMIT_TIMEOUT_MS = 5000;

const input = "num w-full rounded bg-neutral-800 px-1 py-0.5";

export function OrderTicket({ rules }: { rules: SymbolRules }) {
  const live = useSession((s) => s.conn === "live");
  useOrders((s) => s.version);
  const [t, setT] = useState<Omit<TicketInput, "symbol">>({
    side: OrderSide.BUY,
    type: OrderType.LIMIT,
    tif: TimeInForce.GTC,
    price: "",
    stopPrice: "",
    qty: "100",
    displayQty: "",
    expireDate: todayUtc(),
    stpMode: SelfTradePrevention.UNSPECIFIED,
    stpGroup: "",
  });
  const [attempted, setAttempted] = useState(false);
  const [inFlight, setInFlight] = useState<string>();
  const [lastId, setLastId] = useState<string>();
  const ticket: TicketInput = { ...t, symbol: rules.symbol };
  const { errors, draft } = validateTicket(ticket, rules);
  const row = lastId ? useOrders.getState().blotter.rows.get(lastId) : undefined;

  // One order in flight at a time: released by the first report or a timeout.
  useEffect(() => {
    if (!inFlight) return;
    if (row && row.orderId === inFlight && row.state !== "pending") setInFlight(undefined);
    const timer = setTimeout(() => setInFlight(undefined), SUBMIT_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [inFlight, row, row?.state]);

  const set = <K extends keyof typeof t>(k: K, v: (typeof t)[K]) => setT((s) => ({ ...s, [k]: v }));
  const show = (f: Field) => (attempted || (f in t && (t as Record<string, unknown>)[f] !== "") ? errors[f] : undefined);
  const submit = () => {
    setAttempted(true);
    if (!draft || inFlight) return;
    const id = activeClient.current?.placeOrder(draft);
    if (id) {
      setInFlight(id);
      setLastId(id);
      setAttempted(false);
    }
  };
  const shownErrors = (Object.keys(errors) as Field[]).map(show).filter(Boolean);

  return (
    <form
      className="flex w-80 shrink-0 flex-col gap-1.5 border-r border-neutral-800 p-3 text-sm"
      data-testid="ticket"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="flex items-center justify-between text-xs text-neutral-400">
        <span>Order ticket · {rules.symbol}</span>
        <span>
          tick {rules.tick_size} · lot {rules.lot_size ?? 1}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-1">
        {([OrderSide.BUY, OrderSide.SELL] as const).map((side) => (
          <button
            key={side}
            type="button"
            data-testid={`ticket-side-${side === OrderSide.BUY ? "buy" : "sell"}`}
            onClick={() => set("side", side)}
            className={`rounded py-1 ${
              t.side === side ? (side === OrderSide.BUY ? "bg-emerald-700" : "bg-red-700") : "bg-neutral-800 text-neutral-400"
            }`}
          >
            {side === OrderSide.BUY ? "Buy" : "Sell"}
          </button>
        ))}
      </div>
      <div className="grid grid-cols-2 gap-1">
        <label className="text-xs text-neutral-400">
          type
          <select data-testid="ticket-type" className={input} value={t.type} onChange={(e) => set("type", Number(e.target.value))}>
            {TYPES.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label className="text-xs text-neutral-400">
          time in force
          <select data-testid="ticket-tif" className={input} value={t.tif} onChange={(e) => set("tif", Number(e.target.value))}>
            {TIFS.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label className="text-xs text-neutral-400">
          quantity
          <input data-testid="ticket-qty" className={input} inputMode="numeric" value={t.qty} onChange={(e) => set("qty", e.target.value)} />
        </label>
        <label className="text-xs text-neutral-400" title="Iceberg: visible quantity (MaxFloor); empty or 0 shows all">
          display qty
          <input data-testid="ticket-display" className={input} inputMode="numeric" value={t.displayQty} onChange={(e) => set("displayQty", e.target.value)} />
        </label>
        {needsPrice(t.type) && (
          <label className="text-xs text-neutral-400">
            limit price
            <input data-testid="ticket-price" className={input} inputMode="decimal" value={t.price} onChange={(e) => set("price", e.target.value)} />
          </label>
        )}
        {needsStop(t.type) && (
          <label className="text-xs text-neutral-400">
            stop price
            <input data-testid="ticket-stop" className={input} inputMode="decimal" value={t.stopPrice} onChange={(e) => set("stopPrice", e.target.value)} />
          </label>
        )}
        {t.tif === TimeInForce.GTD && (
          <label className="text-xs text-neutral-400">
            expires (UTC)
            <input data-testid="ticket-expiry" type="date" className={input} min={todayUtc()} value={t.expireDate} onChange={(e) => set("expireDate", e.target.value)} />
          </label>
        )}
        <label className="text-xs text-neutral-400">
          STP mode
          <select data-testid="ticket-stp" className={input} value={t.stpMode} onChange={(e) => set("stpMode", Number(e.target.value))}>
            {STPS.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label className="text-xs text-neutral-400">
          STP group
          <input data-testid="ticket-stp-group" className={input} value={t.stpGroup} onChange={(e) => set("stpGroup", e.target.value)} />
        </label>
      </div>
      {shownErrors.length > 0 && (
        <ul className="text-xs text-red-400" data-testid="ticket-errors">
          {shownErrors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
      <button
        type="submit"
        data-testid="ticket-submit"
        disabled={!live || inFlight !== undefined}
        className={`rounded py-1 font-medium disabled:opacity-40 ${t.side === OrderSide.BUY ? "bg-emerald-700 hover:bg-emerald-600" : "bg-red-700 hover:bg-red-600"}`}
      >
        {inFlight ? "Sending…" : `${t.side === OrderSide.BUY ? "Buy" : "Sell"} ${rules.symbol}`}
      </button>
      {row && (
        <div className={`text-xs ${row.state === "rejected" ? "text-red-400" : "text-neutral-400"}`} data-testid="ticket-result">
          {row.orderId}: {row.state}
          {row.state === "rejected" && row.reason ? ` — ${row.reason}` : ""}
        </div>
      )}
    </form>
  );
}
