// SPDX-License-Identifier: MIT
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  ExecutionReportSchema,
  ExecutionType as ET,
  OrderSide,
  OrderStatus as OS,
  OrderType,
  SelfTradePrevention,
  TimeInForce,
  type ExecutionReport,
} from "../gen/fxp_pb";
import { applyReport, emptyBlotter, newOrderId, rebuild, trackCancel, trackNew, trackReplace } from "./orders";
import type { OrderDraft } from "./validate";

let exec = 0;
const er = (orderId: string, executionType: ET, orderStatus: OS, over: Omit<Partial<ExecutionReport>, "$typeName" | "$unknown"> = {}) =>
  create(ExecutionReportSchema, {
    executionId: `E${++exec}`,
    orderId,
    symbol: "DEMO",
    side: OrderSide.BUY,
    executionType,
    orderStatus,
    orderQuantity: 100,
    leavesQuantity: 100,
    ...over,
  });
const draft: OrderDraft = {
  symbol: "DEMO",
  side: OrderSide.BUY,
  type: OrderType.LIMIT,
  tif: TimeInForce.GTC,
  price: 10000n,
  stopPrice: 0n,
  qty: 100,
  displayQty: 0,
  expireDate: "",
  stpMode: SelfTradePrevention.UNSPECIFIED,
  stpGroup: "",
};
const fill = (id: string, qty: number, cum: number, status: OS, price = 10000n) =>
  er(id, ET.EXEC_FILLED, status, { executedQuantity: qty, cumQuantity: cum, leavesQuantity: 100 - cum, executionPrice: price, avgPx: price });

describe("order state machine", () => {
  it("walks new -> partial -> filled and records own fills", () => {
    const b = emptyBlotter();
    trackNew(b, "A", draft);
    expect(b.rows.get("A")?.state).toBe("pending");
    applyReport(b, er("A", ET.EXEC_NEW, OS.ORDER_NEW));
    applyReport(b, fill("A", 40, 40, OS.ORDER_PARTIALLY_FILLED));
    expect(b.rows.get("A")).toMatchObject({ state: "partial", cum: 40, leaves: 60, price: 10000n });
    applyReport(b, fill("A", 60, 100, OS.ORDER_FILLED, 10010n));
    expect(b.rows.get("A")).toMatchObject({ state: "filled", cum: 100, leaves: 0 });
    expect(b.fills.map((f) => f.qty)).toEqual([60, 40]);
  });

  it("drops duplicates and ignores out-of-order reports", () => {
    const b = emptyBlotter();
    trackNew(b, "A", draft);
    const partial = fill("A", 40, 40, OS.ORDER_PARTIALLY_FILLED);
    const filled = fill("A", 60, 100, OS.ORDER_FILLED);
    const ack = er("A", ET.EXEC_NEW, OS.ORDER_NEW);
    applyReport(b, filled);
    expect(applyReport(b, filled)).toBe(false);
    applyReport(b, partial);
    applyReport(b, ack);
    expect(b.rows.get("A")).toMatchObject({ state: "filled", cum: 100, leaves: 0 });
    expect(b.fills).toHaveLength(2);
  });

  it("keeps an IOC remainder cancel terminal when its fill arrives after it", () => {
    const b = emptyBlotter();
    trackNew(b, "I", { ...draft, tif: TimeInForce.IOC });
    applyReport(b, er("I", ET.EXEC_CANCELED, OS.ORDER_CANCELED, { leavesQuantity: 70 }));
    applyReport(b, fill("I", 30, 30, OS.ORDER_PARTIALLY_FILLED));
    expect(b.rows.get("I")).toMatchObject({ state: "canceled", cum: 30, leaves: 0 });
  });

  it("restatement reduces quantity and is flagged", () => {
    const b = emptyBlotter();
    trackNew(b, "S", draft);
    applyReport(b, er("S", ET.EXEC_NEW, OS.ORDER_NEW));
    applyReport(b, er("S", ET.EXEC_RESTATED, OS.ORDER_NEW, { orderQuantity: 60, leavesQuantity: 60, rejectReason: "self-trade prevention" }));
    expect(b.rows.get("S")).toMatchObject({ state: "new", qty: 60, leaves: 60, restated: true, reason: "self-trade prevention" });
  });

  it("expiry and rejects are terminal with reasons", () => {
    const b = emptyBlotter();
    trackNew(b, "X", draft);
    applyReport(b, er("X", ET.EXEC_REJECTED, OS.ORDER_REJECTED, { rejectReason: "price outside symbol tick" }));
    expect(b.rows.get("X")).toMatchObject({ state: "rejected", reason: "price outside symbol tick" });
    trackNew(b, "D", { ...draft, tif: TimeInForce.DAY });
    applyReport(b, er("D", ET.EXEC_EXPIRED, OS.ORDER_EXPIRED));
    expect(b.rows.get("D")?.state).toBe("expired");
  });

  it("replace retires the original and inherits into the replacement", () => {
    const b = emptyBlotter();
    trackNew(b, "A", draft);
    applyReport(b, er("A", ET.EXEC_NEW, OS.ORDER_NEW));
    trackReplace(b, "A", "A2", { price: 10100n, qty: 50 });
    expect(b.rows.get("A")?.pending).toBe("replace");
    applyReport(b, er("A", ET.EXEC_REPLACED, OS.ORDER_REPLACED, { origOrderId: "A", orderQuantity: 50, leavesQuantity: 50 }));
    applyReport(b, er("A2", ET.EXEC_NEW, OS.ORDER_NEW, { origOrderId: "A", orderQuantity: 50, leavesQuantity: 50 }));
    expect(b.rows.get("A")).toMatchObject({ state: "replaced", leaves: 0, replacedBy: "A2", pending: undefined });
    expect(b.rows.get("A2")).toMatchObject({ state: "new", price: 10100n, qty: 50, origOrderId: "A", type: OrderType.LIMIT });
  });

  it("a replacement that inherits fills is partial", () => {
    const b = emptyBlotter();
    trackNew(b, "P", draft);
    applyReport(b, fill("P", 40, 40, OS.ORDER_PARTIALLY_FILLED));
    trackReplace(b, "P", "P2", { price: 10100n });
    applyReport(b, er("P", ET.EXEC_REPLACED, OS.ORDER_REPLACED, { origOrderId: "P", cumQuantity: 40, leavesQuantity: 60 }));
    applyReport(b, er("P2", ET.EXEC_NEW, OS.ORDER_NEW, { origOrderId: "P", cumQuantity: 40, leavesQuantity: 60 }));
    expect(b.rows.get("P2")).toMatchObject({ state: "partial", cum: 40, leaves: 60, price: 10100n });
  });

  it("request rejects leave the order intact", () => {
    const b = emptyBlotter();
    trackNew(b, "F", draft);
    applyReport(b, fill("F", 100, 100, OS.ORDER_FILLED));
    trackCancel(b, "F");
    applyReport(b, er("F", ET.EXEC_REJECTED, OS.ORDER_REJECTED, { origOrderId: "F", rejectReason: "Unknown order (filled, cancelled or never existed)" }));
    expect(b.rows.get("F")).toMatchObject({ state: "filled", pending: undefined });
    expect(b.rows.get("F")?.requestReject).toMatch(/cancel rejected: Unknown order/);

    trackNew(b, "W", draft);
    applyReport(b, er("W", ET.EXEC_NEW, OS.ORDER_NEW));
    trackReplace(b, "W", "W2", { qty: 10 });
    applyReport(b, er("W", ET.EXEC_REJECTED, OS.ORDER_REJECTED, { origOrderId: "W", rejectReason: "Replace quantity not above filled quantity" }));
    expect(b.rows.get("W")).toMatchObject({ state: "new", pending: undefined });
    expect(b.rows.get("W")?.requestReject).toMatch(/replace rejected/);
    expect(b.replaces.size).toBe(0);
    expect(b.rows.has("W2")).toBe(false);

    // The gateway rejects an invalid replace under the replacement id.
    trackReplace(b, "W", "W3", { qty: 10 });
    applyReport(b, er("W3", ET.EXEC_REJECTED, OS.ORDER_REJECTED, { rejectReason: "unknown time_in_force 99" }));
    expect(b.rows.has("W3")).toBe(false);
    expect(b.rows.get("W")?.requestReject).toMatch(/replace rejected: unknown time_in_force/);
  });

  it("rebuilds from order status, keeping local prices and dropping orders the core lost", () => {
    const b = emptyBlotter();
    trackNew(b, "A", draft);
    trackNew(b, "LOST", draft);
    b.stale = true;
    rebuild(b, [
      er("A", ET.EXEC_ORDER_STATUS, OS.ORDER_PARTIALLY_FILLED, { cumQuantity: 30, leavesQuantity: 70, avgPx: 10000n }),
      er("OTHER", ET.EXEC_ORDER_STATUS, OS.ORDER_FILLED, { cumQuantity: 100, leavesQuantity: 0 }),
    ]);
    expect(b.stale).toBe(false);
    expect(b.rows.get("A")).toMatchObject({ state: "partial", cum: 30, leaves: 70, price: 10000n });
    expect(b.rows.get("OTHER")?.state).toBe("filled");
    expect(b.rows.get("OTHER")?.price).toBeUndefined();
    expect(b.rows.has("LOST")).toBe(false);
    expect(b.log[0]?.text).toMatch(/2 orders, 1 unknown to the core dropped/);
  });

  it("generates unique client order ids", () => {
    const ids = new Set(Array.from({ length: 1000 }, () => newOrderId(0)));
    expect(ids.size).toBe(1000);
  });
});
