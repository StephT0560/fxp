// SPDX-License-Identifier: MIT
import { describe, expect, it } from "vitest";
import { parseFixPreview } from "./fix-preview";
describe("FIX preview fields", () => {
  it("keeps authoritative field order, unknown tags and empty values", () => {
    expect(parseFixPreview("8=FIX.4.4|9999=custom|1=||55=DEMO|10=123|")).toEqual([
      { tag: "8", name: "BeginString", value: "FIX.4.4" },
      { tag: "9999", name: "", value: "custom" },
      { tag: "1", name: "Account", value: "" },
      { tag: "55", name: "Symbol", value: "DEMO" },
      { tag: "10", name: "CheckSum", value: "123" },
    ]);
  });
  it("preserves equals within values and repeated tags", () => {
    expect(parseFixPreview("55=A=B|55=C").map((f) => f.value)).toEqual(["A=B", "C"]);
    expect(parseFixPreview("||")).toEqual([]);
  });
});
