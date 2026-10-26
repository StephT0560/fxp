// SPDX-License-Identifier: MIT
import { OrderSide, OrderType, SelfTradePrevention, TimeInForce } from "../gen/fxp_pb";
import { formatPrice } from "./format";

/** Per-symbol rules as served by /api/symbols. */
export interface SymbolRules {
  symbol: string;
  price_scale: number;
  tick_size: string;
  lot_size?: number;
  min_quantity?: number;
  max_quantity?: number;
}

export const INT32_MAX = 2_147_483_647n;
export const INT64_MAX = 9_223_372_036_854_775_807n;

export interface TicketInput {
  symbol: string;
  side: OrderSide;
  type: OrderType;
  tif: TimeInForce;
  price: string;
  stopPrice: string;
  qty: string;
  displayQty: string;
  expireDate: string;
  stpMode: SelfTradePrevention;
  stpGroup: string;
}

/** A validated order, prices in minor units. */
export interface OrderDraft {
  symbol: string;
  side: OrderSide;
  type: OrderType;
  tif: TimeInForce;
  price: bigint;
  stopPrice: bigint;
  qty: number;
  displayQty: number;
  expireDate: string;
  stpMode: SelfTradePrevention;
  stpGroup: string;
}

export type Field = "price" | "stopPrice" | "qty" | "displayQty" | "expireDate" | "stpGroup" | "tif";
export type Errors = Partial<Record<Field, string>>;

export const needsPrice = (t: OrderType) => t === OrderType.LIMIT || t === OrderType.STOP_LIMIT;
export const needsStop = (t: OrderType) => t === OrderType.STOP || t === OrderType.STOP_LIMIT;

/** Parses a decimal price into minor units without floating point. */
export function parsePrice(text: string, rules: SymbolRules): bigint | string {
  const t = text.trim();
  if (t === "") return "required";
  const m = /^(\d+)(?:\.(\d*))?$/.exec(t);
  if (!m) return "must be a positive decimal number";
  const scale = rules.price_scale;
  const frac = m[2] ?? "";
  if (frac.length > scale) return scale === 0 ? "must be a whole number" : `at most ${scale} decimal places`;
  const v = BigInt(m[1]! + frac.padEnd(scale, "0"));
  if (v <= 0n) return "must be greater than zero";
  if (v > INT64_MAX) return "too large";
  const tick = BigInt(rules.tick_size || "1");
  if (tick > 1n && v % tick !== 0n) return `must be a multiple of the tick ${formatPrice(tick, scale)}`;
  return v;
}

/** Parses an order quantity against the symbol's lot, min and max. */
export function parseQty(text: string, rules: SymbolRules): number | string {
  const t = text.trim();
  if (!/^\d+$/.test(t)) return "must be a whole number";
  const v = BigInt(t);
  if (v <= 0n) return "must be greater than zero";
  if (v > INT32_MAX) return "too large";
  const lot = BigInt(rules.lot_size || 1);
  const min = BigInt(rules.min_quantity || 1);
  const max = BigInt(rules.max_quantity || INT32_MAX);
  if (v < min) return `minimum ${min}`;
  if (v > max) return `maximum ${max}`;
  if (v % lot !== 0n) return `must be a multiple of the lot ${lot}`;
  return Number(v);
}

export function todayUtc(now = new Date()): string {
  return now.toISOString().slice(0, 10);
}

function validDate(s: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}

/** Client-side checks only; the core stays authoritative and its rejects are shown as sent. */
export function validateTicket(input: TicketInput, rules: SymbolRules, today = todayUtc()): { errors: Errors; draft?: OrderDraft } {
  const errors: Errors = {};
  let price = 0n;
  let stopPrice = 0n;
  if (needsPrice(input.type)) {
    const p = parsePrice(input.price, rules);
    if (typeof p === "string") errors.price = `Limit price ${p}`;
    else price = p;
  }
  if (needsStop(input.type)) {
    const p = parsePrice(input.stopPrice, rules);
    if (typeof p === "string") errors.stopPrice = `Stop price ${p}`;
    else stopPrice = p;
  }
  const q = parseQty(input.qty, rules);
  let qty = 0;
  if (typeof q === "string") errors.qty = `Quantity ${q}`;
  else qty = q;

  let displayQty = 0;
  const shown = input.displayQty.trim();
  if (shown !== "" && shown !== "0") {
    const d = parseQty(shown, { ...rules, min_quantity: 1 });
    if (typeof d === "string") errors.displayQty = `Display quantity ${d}`;
    else if (input.type !== OrderType.LIMIT) errors.displayQty = "Iceberg needs a limit order";
    else if (input.tif === TimeInForce.IOC || input.tif === TimeInForce.FOK) errors.displayQty = "Iceberg needs a resting time in force";
    else if (qty > 0 && d > qty) errors.displayQty = "Display quantity cannot exceed the order quantity";
    else displayQty = d;
  }

  const expireDate = input.tif === TimeInForce.GTD ? input.expireDate.trim() : "";
  if (input.tif === TimeInForce.GTD) {
    if (!validDate(expireDate)) errors.expireDate = "Expiry date must be YYYY-MM-DD";
    else if (expireDate < today) errors.expireDate = "Expiry date is in the past";
  }
  if ((input.tif === TimeInForce.AT_OPEN || input.tif === TimeInForce.AT_CLOSE) && needsStop(input.type)) {
    errors.tif = "Auction time in force needs a limit or market order";
  }
  const stpGroup = input.stpGroup.trim();
  if (stpGroup !== "" && !/^[A-Za-z0-9_-]{1,32}$/.test(stpGroup)) {
    errors.stpGroup = "STP group: up to 32 letters, digits, - or _";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    draft: { symbol: input.symbol, side: input.side, type: input.type, tif: input.tif, price, stopPrice, qty, displayQty, expireDate, stpMode: input.stpMode, stpGroup },
  };
}
