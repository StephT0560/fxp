// SPDX-License-Identifier: MIT
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { chartRows, parseBench } from "./bench";
const fixture = () =>
  JSON.parse(readFileSync("../benchmarks/console.json", "utf8"));
describe("recorded latency", () => {
  it("keeps intended and post-send ACK separate and converts ns to ms with asymmetric spread", () => {
    const s = parseBench(fixture()).scenarios[0]!;
    const rows = chartRows(s);
    expect(rows[0]!.intended).toBe(0.711167);
    expect(rows[0]!.post).toBe(0.201599);
    expect(rows[1]!.intended + rows[1]!.intendedError[1]!).toBeCloseTo(
      108.527615,
    );
    expect(rows).toHaveLength(2);
  });
  it.each([null, {}, { schema_version: 1, scenarios: [] }])(
    "rejects missing data: %j",
    (d) => expect(() => parseBench(d)).toThrow(),
  );
  it("rejects mixed network modes, build mismatch, absent and invalid spreads", () => {
    for (const mutate of [
      (s: any) => {
        s.network = "WAN";
      },
      (s: any) => {
        s.environment.revision = "other";
      },
      (s: any) => {
        delete s.summary.ack_after_send_p50_ns;
      },
      (s: any) => {
        s.summary.ack_p99_ns.min = -1;
      },
      (s: any) => {
        s.summary.ack_p99_ns.max = NaN;
      },
      (s: any) => {
        s.summary.ack_p99_ns.max = 0;
      },
    ]) {
      const d = fixture();
      mutate(d.scenarios[0]);
      expect(() => parseBench(d)).toThrow();
    }
  });
});
