// SPDX-License-Identifier: MIT
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { captureFrame, inspectFrame, redact, useInspector } from "./inspector";

describe("inspector credential boundary", () => {
  it("redacts nested token keys and JWT/bearer/opaque token-like values", () => {
    const d = redact({
      jwt: "private",
      nested: [
        {
          accessToken: "private",
          password: "private",
          api_key: "private",
          text: "Bearer abc",
          note: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.signature",
          opaque: "a".repeat(48),
        },
      ],
      orderId: "order-1",
    });
    expect(JSON.stringify(d)).not.toMatch(/private|Bearer|signature|aaaa/);
    expect(JSON.stringify(d)).toContain("order-1");
  });
  it("never stores Logon JWTs in decoded, raw hex or comparison bytes", () => {
    const jwt = "fixture-not-a-credential";
    const r = inspectFrame(
      new Uint8Array(readFileSync("src/testdata/golden/logon.bin")),
      "sent",
    );
    expect(r.type).toBe("logon");
    expect(r.decoded).toContain("[REDACTED]");
    expect(r.hex).toContain("Withheld");
    expect(r.orderFrame).toBeUndefined();
    expect(JSON.stringify(r)).not.toContain(jwt);
  });
  it("decodes an independent Go golden, and withholds unknown/malformed bytes", () => {
    const data = new Uint8Array(readFileSync("src/testdata/golden/trade.bin"));
    const r = inspectFrame(data, "received");
    expect(r.type).toBe("tradeCaptureReport");
    expect(r.hex).not.toContain("Withheld");
    // Unknown field 127, length-delimited credential. Known payload still decodes.
    const unknown = new Uint8Array([
      ...data,
      0xfa,
      0x07,
      6,
      ...new TextEncoder().encode("secret"),
    ]);
    const u = inspectFrame(unknown, "received");
    expect(u.hex).toContain("Withheld");
    expect(JSON.stringify(u)).not.toContain("secret");
    expect(inspectFrame(new Uint8Array([0xff]), "received").hex).toContain(
      "Withheld",
    );
  });
  it("retains exact comparison bytes from an independent new-order golden", () => {
    const raw = new Uint8Array(
      readFileSync("src/testdata/golden/new_order.bin"),
    );
    const r = inspectFrame(raw, "sent");
    expect(r.type).toBe("newOrder");
    expect(r.symbol).toBe("DEMO");
    expect(r.orderFrame).toBe(Buffer.from(raw).toString("base64"));
  });
  it("bounds history and clears all retained frames", () => {
    useInspector.getState().clear();
    for (let i = 0; i < 205; i++)
      captureFrame(new Uint8Array([0xff]), "received");
    expect(useInspector.getState().rows).toHaveLength(200);
    useInspector.getState().clear();
    expect(useInspector.getState().rows).toHaveLength(0);
  });
});
