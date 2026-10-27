// SPDX-License-Identifier: MIT
import { fromBinary, toBinary, toJson } from "@bufbuild/protobuf";
import { create } from "zustand";
import { FXPMessageSchema } from "../gen/fxp_pb";
import { schedule } from "./scheduler";

const REDACTED = "[REDACTED]";
const sensitiveKey =
  /token|jwt|secret|password|authorization|credential|api.?key/i;
const sensitiveValue =
  /(?:eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)|(?:bearer\s+\S+)|(?:[A-Za-z0-9_+\/-]{40,}={0,2})/i;
export function redact(value: unknown, key = ""): unknown {
  if (sensitiveKey.test(key)) return REDACTED;
  if (typeof value === "string")
    return sensitiveValue.test(value) ? REDACTED : value;
  if (Array.isArray(value)) return value.map((v) => redact(v));
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.entries(value).map(([k, v]) => [k, redact(v, k)]),
    );
  return value;
}
export type Direction = "sent" | "received";
export interface InspectedFrame {
  id: number;
  at: string;
  monotonicMs: number;
  direction: Direction;
  type: string;
  symbol: string;
  bytes: number;
  decoded: string;
  hex: string;
  orderFrame?: string;
}
let nextId = 0;
export function inspectFrame(
  data: Uint8Array,
  direction: Direction,
): InspectedFrame {
  const row: InspectedFrame = {
    id: ++nextId,
    at: new Date().toISOString(),
    monotonicMs: performance.now(),
    direction,
    type: "malformed",
    symbol: "",
    bytes: data.length,
    decoded: "Undecodable frame; bytes withheld to protect credentials.",
    hex: "Withheld",
  };
  if (data.length > 65536) {
    row.type = "oversize";
    row.decoded =
      "Frame exceeds the inspector's 64 KiB capture limit; bytes withheld.";
    return row;
  }
  try {
    const msg = fromBinary(FXPMessageSchema, data, {
      readUnknownFields: false,
    });
    row.type = msg.payload.case ?? "unknown";
    const json = toJson(FXPMessageSchema, msg);
    const safe = redact(json);
    row.decoded = JSON.stringify(safe, null, 2);
    const p = msg.payload.value;
    const symbol = p && "symbol" in p ? p.symbol : "";
    row.symbol = typeof symbol === "string" ? String(redact(symbol)) : "";
    const canonical = toBinary(FXPMessageSchema, msg);
    // Unknown fields and noncanonical bytes could conceal credentials. Never
    // retain their raw bytes, even when a known payload decoded successfully.
    const rawSafe =
      row.type !== "logon" &&
      row.type !== "unknown" &&
      JSON.stringify(json) === JSON.stringify(safe) &&
      canonical.length === data.length &&
      canonical.every((b, i) => b === data[i]);
    if (rawSafe) {
      row.hex = Array.from(data, (b) => b.toString(16).padStart(2, "0")).join(
        " ",
      );
      if (row.type === "newOrder")
        row.orderFrame = btoa(String.fromCharCode(...data));
    } else
      row.hex =
        "Withheld: redacted, unknown or noncanonical fields. Decoded view is sanitized.";
  } catch {
    /* Never retain an undecodable credential-bearing frame. */
  }
  return row;
}
const MAX_FRAMES = 200;
export const useInspector = create<{
  rows: InspectedFrame[];
  version: number;
  clear: () => void;
}>((set) => ({
  rows: [],
  version: 0,
  clear: () => set((s) => ({ rows: [], version: s.version + 1 })),
}));
const notify = () => useInspector.setState((s) => ({ version: s.version + 1 }));
export function captureFrame(
  data: Uint8Array | ArrayBuffer,
  direction: Direction,
): void {
  const row = inspectFrame(
    data instanceof Uint8Array ? data : new Uint8Array(data),
    direction,
  );
  const rows = useInspector.getState().rows;
  rows.push(row);
  if (rows.length > MAX_FRAMES) rows.splice(0, rows.length - MAX_FRAMES);
  schedule(notify);
}
