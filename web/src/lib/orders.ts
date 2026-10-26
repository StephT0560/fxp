// SPDX-License-Identifier: MIT
import {
  ExecutionType as ET,
  OrderStatus as OS,
  OrderType,
  type ExecutionReport,
  type OrderMassCancelReport,
  type OrderSide,
  type TimeInForce,
} from "../gen/fxp_pb";
import type { OrderDraft } from "./validate";

export type OrderState = "pending" | "new" | "partial" | "filled" | "canceled" | "rejected" | "expired" | "replaced";
const TERMINAL: ReadonlySet<OrderState> = new Set(["filled", "canceled", "rejected", "expired", "replaced"]);
export const isTerminal = (s: OrderState) => TERMINAL.has(s);

export function stateOf(status: OS): OrderState | undefined {
  switch (status) {
    case OS.ORDER_PENDING:
      return "pending";
    case OS.ORDER_NEW:
      return "new";
    case OS.ORDER_PARTIALLY_FILLED:
      return "partial";
    case OS.ORDER_FILLED:
      return "filled";
    case OS.ORDER_CANCELED:
      return "canceled";
    case OS.ORDER_REJECTED:
      return "rejected";
    case OS.ORDER_REPLACED:
      return "replaced";
    case OS.ORDER_EXPIRED:
      return "expired";
    default:
      return undefined; // pending cancel/replace: no state change
  }
}

// A replacement inherits the original's fills but is reported as ORDER_NEW.
const withFills = (s: OrderState | undefined, cum: number): OrderState | undefined => (s === "new" && cum > 0 ? "partial" : s);

export interface OrderRow {
  orderId: string;
  symbol: string;
  side: OrderSide;
  /** Known only for orders entered in this console; reports carry no limit price. */
  type?: OrderType;
  tif?: TimeInForce;
  price?: bigint;
  stopPrice?: bigint;
  qty: number;
  displayQty: number;
  cum: number;
  leaves: number;
  avgPx: bigint;
  state: OrderState;
  /** Quantity reduced by self-trade prevention (EXEC_RESTATED). */
  restated: boolean;
  /** Reject, cancel, expiry or restatement reason. */
  reason?: string;
  /** Last rejected cancel/replace request; the order itself is unchanged. */
  requestReject?: string;
  pending?: "cancel" | "replace";
  origOrderId?: string;
  replacedBy?: string;
  updated: number;
}

export interface OwnFill {
  key: string;
  orderId: string;
  symbol: string;
  side: OrderSide;
  price: bigint;
  qty: number;
  auction: boolean;
  timeMs: number;
}

export interface LogEntry {
  key: number;
  timeMs: number;
  kind: "report" | "request" | "reject" | "info";
  text: string;
}

interface PendingReplace {
  orig: string;
  price?: bigint;
  qty?: number;
  displayQty?: number;
}

export interface Blotter {
  rows: Map<string, OrderRow>;
  replaces: Map<string, PendingReplace>;
  seen: Set<string>;
  fills: OwnFill[];
  log: LogEntry[];
  /** True between a socket drop and the order-status rebuild. */
  stale: boolean;
  serial: number;
}

export const LOG_LIMIT = 300;
export const FILL_LIMIT = 200;
const SEEN_LIMIT = 20_000;

export function emptyBlotter(): Blotter {
  return { rows: new Map(), replaces: new Map(), seen: new Set(), fills: [], log: [], stale: false, serial: 0 };
}

export function note(b: Blotter, kind: LogEntry["kind"], text: string, timeMs = Date.now()): void {
  b.log.unshift({ key: ++b.serial, timeMs, kind, text });
  if (b.log.length > LOG_LIMIT) b.log.length = LOG_LIMIT;
}

const etName = (et: ET) => (ET[et] ?? String(et)).replace(/^EXEC_/, "");
const osName = (os: OS) => (OS[os] ?? String(os)).replace(/^ORDER_/, "");

function fromReport(r: ExecutionReport, now: number): OrderRow {
  return {
    orderId: r.orderId,
    symbol: r.symbol,
    side: r.side,
    qty: r.orderQuantity || r.cumQuantity + r.leavesQuantity,
    displayQty: r.displayQuantity,
    cum: r.cumQuantity,
    leaves: r.leavesQuantity,
    avgPx: r.avgPx,
    state: withFills(stateOf(r.orderStatus), r.cumQuantity) ?? "new",
    restated: false,
    updated: now,
  };
}

export function trackNew(b: Blotter, orderId: string, d: OrderDraft, now = Date.now()): OrderRow {
  const row: OrderRow = {
    orderId,
    symbol: d.symbol,
    side: d.side,
    type: d.type,
    tif: d.tif,
    price: d.type === OrderType.MARKET || d.type === OrderType.STOP ? undefined : d.price,
    stopPrice: d.stopPrice > 0n ? d.stopPrice : undefined,
    qty: d.qty,
    displayQty: d.displayQty,
    cum: 0,
    leaves: d.qty,
    avgPx: 0n,
    state: "pending",
    restated: false,
    updated: now,
  };
  b.rows.set(orderId, row);
  note(b, "request", `new ${orderId} ${d.symbol} qty ${d.qty}`, now);
  return row;
}

export function trackCancel(b: Blotter, orderId: string, now = Date.now()): void {
  const row = b.rows.get(orderId);
  if (row) Object.assign(row, { pending: "cancel", requestReject: undefined, updated: now });
  note(b, "request", `cancel ${orderId}`, now);
}

export function trackReplace(b: Blotter, orderId: string, newId: string, change: Omit<PendingReplace, "orig">, now = Date.now()): void {
  const row = b.rows.get(orderId);
  if (row) Object.assign(row, { pending: "replace", requestReject: undefined, updated: now });
  b.replaces.set(newId, { orig: orderId, ...change });
  note(b, "request", `replace ${orderId} -> ${newId}`, now);
}

/**
 * Applies one private ExecutionReport. Duplicates are dropped by execution id,
 * cumulative quantity never goes backwards, and a terminal order only moves to
 * another terminal state, so late or reordered reports cannot resurrect it.
 * Returns false for a duplicate.
 */
export function applyReport(b: Blotter, r: ExecutionReport, now = Date.now()): boolean {
  if (r.executionId) {
    const key = `${r.executionId}/${r.executionType}`;
    if (b.seen.has(key)) return false;
    if (b.seen.size >= SEEN_LIMIT) b.seen.clear();
    b.seen.add(key);
  }
  const et = r.executionType;
  const reason = r.rejectReason ? ` — ${r.rejectReason}` : "";

  // A rejected cancel or replace names its target in orig_order_id (core) or,
  // for a gateway-rejected replace, the replacement id we are still tracking.
  const pendingReplace = b.replaces.get(r.orderId);
  if (et === ET.EXEC_REJECTED && (r.origOrderId !== "" || pendingReplace)) {
    const targetId = r.origOrderId || pendingReplace!.orig;
    const row = b.rows.get(targetId);
    const kind = pendingReplace || row?.pending === "replace" ? "replace" : "cancel";
    if (pendingReplace) b.replaces.delete(r.orderId);
    if (kind === "replace") for (const [id, p] of b.replaces) if (p.orig === targetId) b.replaces.delete(id);
    if (row) Object.assign(row, { pending: undefined, requestReject: `${kind} rejected: ${r.rejectReason || "rejected"}`, updated: now });
    note(b, "reject", `${kind} of ${targetId} rejected${reason}`, now);
    return true;
  }

  let row = b.rows.get(r.orderId);
  if (!row) {
    row = fromReport(r, now);
    if (pendingReplace) {
      const orig = b.rows.get(pendingReplace.orig);
      if (orig) {
        Object.assign(row, {
          type: orig.type,
          tif: orig.tif,
          price: pendingReplace.price ?? orig.price,
          stopPrice: orig.stopPrice,
          displayQty: pendingReplace.displayQty ?? orig.displayQty,
        });
        orig.replacedBy = r.orderId;
      }
      row.origOrderId = pendingReplace.orig;
      b.replaces.delete(r.orderId);
    } else if (r.origOrderId && r.origOrderId !== r.orderId) {
      row.origOrderId = r.origOrderId;
    }
    b.rows.set(r.orderId, row);
  }

  if (et === ET.EXEC_FILLED && r.executedQuantity > 0) {
    b.fills.unshift({
      key: r.executionId || `${r.orderId}/${r.cumQuantity}`,
      orderId: r.orderId,
      symbol: r.symbol,
      side: r.side,
      price: r.executionPrice,
      qty: r.executedQuantity,
      auction: r.auction,
      timeMs: now,
    });
    if (b.fills.length > FILL_LIMIT) b.fills.length = FILL_LIMIT;
  }

  const terminal = isTerminal(row.state);
  const next = withFills(stateOf(r.orderStatus), Math.max(r.cumQuantity, row.cum));
  if (r.cumQuantity >= row.cum) {
    if (r.cumQuantity > row.cum || !terminal) {
      row.cum = r.cumQuantity;
      if (r.avgPx > 0n) row.avgPx = r.avgPx;
      if (r.orderQuantity > 0) row.qty = r.orderQuantity;
    }
    if (!terminal) row.leaves = r.leavesQuantity;
    if (next && (!terminal || isTerminal(next))) row.state = next;
  }
  if (isTerminal(row.state)) {
    row.leaves = 0;
    row.pending = undefined;
  }
  if (et === ET.EXEC_REPLACED) {
    for (const [id, p] of b.replaces) if (p.orig === row.orderId) row.replacedBy = id;
  }
  if (et === ET.EXEC_RESTATED) {
    row.restated = true;
    row.reason = r.rejectReason || "restated by self-trade prevention";
  } else if (r.rejectReason) {
    row.reason = r.rejectReason;
  }
  row.updated = now;
  note(
    b,
    "report",
    `${etName(et)} ${r.orderId} ${osName(r.orderStatus)} cum ${r.cumQuantity} leaves ${r.leavesQuantity}${
      r.executedQuantity > 0 ? ` (+${r.executedQuantity}${r.auction ? " auction" : ""})` : ""
    }${reason}`,
    now,
  );
  return true;
}

/**
 * Replaces the blotter with an order-status snapshot after (re)connecting.
 * Static fields the reports lack (limit price, type) are kept for orders this
 * console entered; working orders the core no longer knows are dropped.
 */
export function rebuild(b: Blotter, reports: ExecutionReport[], now = Date.now()): void {
  const old = b.rows;
  b.rows = new Map();
  b.replaces.clear();
  for (const r of reports) {
    const row = fromReport(r, now);
    const prev = old.get(r.orderId);
    if (prev) {
      Object.assign(row, { type: prev.type, tif: prev.tif, price: prev.price, stopPrice: prev.stopPrice, restated: prev.restated, reason: prev.reason });
    }
    if (r.origOrderId && r.origOrderId !== r.orderId) row.origOrderId = r.origOrderId;
    if (isTerminal(row.state)) row.leaves = 0;
    b.rows.set(r.orderId, row);
  }
  const dropped = [...old.values()].filter((r) => !isTerminal(r.state) && !b.rows.has(r.orderId)).length;
  b.stale = false;
  note(b, "info", `blotter rebuilt from order status: ${reports.length} orders${dropped ? `, ${dropped} unknown to the core dropped` : ""}`, now);
}

export function onMassCancelReport(b: Blotter, r: OrderMassCancelReport, now = Date.now()): string {
  const scope = r.symbol || "all symbols";
  const text = r.rejectReason ? `cancel-all (${scope}) rejected — ${r.rejectReason}` : `cancel-all (${scope}): ${r.totalAffected} canceled`;
  note(b, r.rejectReason ? "reject" : "info", text, now);
  return text;
}

let counter = 0;
/** Client order ids: unique per console session, short, never reused. */
export function newOrderId(now = Date.now()): string {
  const rand = new Uint32Array(1);
  globalThis.crypto.getRandomValues(rand);
  return `c${now.toString(36)}${(rand[0]! % 1296).toString(36).padStart(2, "0")}${(++counter).toString(36)}`;
}
