// SPDX-License-Identifier: MIT
import { describe, expect, it } from "vitest";
import { OrderSide, OrderType, SelfTradePrevention, TimeInForce } from "../gen/fxp_pb";
import { parsePrice, parseQty, validateTicket, type SymbolRules, type TicketInput } from "./validate";

const rules: SymbolRules = { symbol: "T", price_scale: 2, tick_size: "5", lot_size: 10, min_quantity: 10, max_quantity: 1000 };
const ticket = (over: Partial<TicketInput> = {}): TicketInput => ({
  symbol: "T",
  side: OrderSide.BUY,
  type: OrderType.LIMIT,
  tif: TimeInForce.GTC,
  price: "100.05",
  stopPrice: "",
  qty: "100",
  displayQty: "",
  expireDate: "",
  stpMode: SelfTradePrevention.UNSPECIFIED,
  stpGroup: "",
  ...over,
});

describe("price and quantity", () => {
  it("parses decimals exactly into minor units", () => {
    expect(parsePrice("100.05", rules)).toBe(10005n);
    expect(parsePrice("100.1", rules)).toBe(10010n);
    expect(parsePrice("92233720368547758.05", rules)).toBe(9223372036854775805n);
  });
  it("rejects scale, tick, sign and overflow problems", () => {
    expect(parsePrice("100.001", rules)).toMatch(/2 decimal places/);
    expect(parsePrice("100.03", rules)).toMatch(/tick 0.05/);
    expect(parsePrice("-1", rules)).toMatch(/positive/);
    expect(parsePrice("0", rules)).toMatch(/greater than zero/);
    expect(parsePrice("92233720368547758.10", rules)).toBe("too large");
    expect(parsePrice("1e3", rules)).toMatch(/positive decimal/);
  });
  it("enforces lot, min, max and int32", () => {
    expect(parseQty("100", rules)).toBe(100);
    expect(parseQty("105", rules)).toMatch(/lot 10/);
    expect(parseQty("0", rules)).toMatch(/greater than zero/);
    expect(parseQty("5", { ...rules, lot_size: 1 })).toMatch(/minimum 10/);
    expect(parseQty("2000", rules)).toMatch(/maximum 1000/);
    expect(parseQty("2147483648", { ...rules, max_quantity: undefined })).toBe("too large");
    expect(parseQty("1.5", rules)).toMatch(/whole number/);
  });
});

describe("ticket", () => {
  it("builds a draft for a valid limit order", () => {
    const { draft, errors } = validateTicket(ticket(), rules, "2030-01-01");
    expect(errors).toEqual({});
    expect(draft).toMatchObject({ price: 10005n, qty: 100, stopPrice: 0n, displayQty: 0 });
  });
  it("requires the prices each type uses", () => {
    expect(validateTicket(ticket({ type: OrderType.MARKET, price: "" }), rules).errors).toEqual({});
    expect(validateTicket(ticket({ type: OrderType.STOP }), rules).errors.stopPrice).toMatch(/Stop price required/);
    const sl = validateTicket(ticket({ type: OrderType.STOP_LIMIT, price: "", stopPrice: "99.00" }), rules);
    expect(sl.errors.price).toMatch(/Limit price required/);
  });
  it("checks GTD expiry", () => {
    expect(validateTicket(ticket({ tif: TimeInForce.GTD, expireDate: "" }), rules, "2030-01-01").errors.expireDate).toMatch(/YYYY-MM-DD/);
    expect(validateTicket(ticket({ tif: TimeInForce.GTD, expireDate: "2029-12-31" }), rules, "2030-01-01").errors.expireDate).toMatch(/past/);
    expect(validateTicket(ticket({ tif: TimeInForce.GTD, expireDate: "2030-02-30" }), rules, "2030-01-01").errors.expireDate).toBeDefined();
    const ok = validateTicket(ticket({ tif: TimeInForce.GTD, expireDate: "2030-01-01" }), rules, "2030-01-01");
    expect(ok.draft?.expireDate).toBe("2030-01-01");
    expect(validateTicket(ticket({ expireDate: "2030-01-01" }), rules).draft?.expireDate).toBe("");
  });
  it("checks iceberg display quantity", () => {
    expect(validateTicket(ticket({ displayQty: "20" }), rules).draft?.displayQty).toBe(20);
    expect(validateTicket(ticket({ displayQty: "25" }), rules).errors.displayQty).toMatch(/lot/);
    expect(validateTicket(ticket({ displayQty: "200" }), rules).errors.displayQty).toMatch(/exceed/);
    expect(validateTicket(ticket({ displayQty: "20", tif: TimeInForce.IOC }), rules).errors.displayQty).toMatch(/resting/);
    expect(validateTicket(ticket({ displayQty: "20", type: OrderType.MARKET }), rules).errors.displayQty).toMatch(/limit/);
  });
  it("checks STP group and auction time in force", () => {
    expect(validateTicket(ticket({ stpMode: SelfTradePrevention.CANCEL_BOTH, stpGroup: "desk-1" }), rules).draft?.stpGroup).toBe("desk-1");
    expect(validateTicket(ticket({ stpGroup: "bad group!" }), rules).errors.stpGroup).toBeDefined();
    expect(validateTicket(ticket({ type: OrderType.STOP, stopPrice: "99.00", tif: TimeInForce.AT_OPEN }), rules).errors.tif).toBeDefined();
  });
});
