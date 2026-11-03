// SPDX-License-Identifier: MIT
import { createContext, useContext } from "react";
import type { SymbolRules } from "./validate";

export interface RunConfig {
  gatewayUrl: string;
  token: string;
  runId: string;
  readOnly: boolean;
  symbols?: SymbolRules[];
  apiUrl?: string;
  features?: { demoControls?: boolean };
  notifyTokenExpired?: boolean;
  parentOrigin?: string;
}
export interface RunSession extends RunConfig {
  expiresAt: number;
  expire: () => void;
}
export const RunContext = createContext<RunSession | undefined>(undefined);
export const useRun = () => useContext(RunContext);
export const RUN_ENDED = "Run ended or token expired";
export const DEFAULT_RUN_SYMBOLS: SymbolRules[] = ["AAPL", "MSFT", "GOOGL", "AMZN", "NVDA", "TSLA", "META", "JPM", "ETH/USD", "BTC/USD"].map(symbol => ({ symbol, price_scale: 2, tick_size: "1" }));

declare global {
  interface Window {
    __FXP_CONSOLE__?: unknown;
    __FXP_CONSOLE_ALLOWED_ORIGINS__?: unknown;
  }
}

const object = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
function invalid(): never { throw Error("Invalid run configuration"); }
export function exactOrigin(value: unknown): string {
  if (typeof value !== "string" || value.length > 255) return invalid();
  const url = new URL(value);
  if (!["http:", "https:"].includes(url.protocol) || url.origin !== value) return invalid();
  return value;
}
function endpoint(value: unknown, websocket: boolean, token: string): string {
  if (typeof value !== "string" || value.length > 2048) return invalid();
  const u = new URL(value);
  const secure = websocket ? "wss:" : "https:";
  const local = websocket ? "ws:" : "http:";
  if ((u.protocol !== secure && !(u.protocol === local && ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname))) || u.username || u.password || u.search || u.hash || value.includes(token)) return invalid();
  return u.href;
}
export function tokenExpiry(token: string): number {
  try {
    if (token.length > 16384 || !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(token)) return invalid();
    const body = token.split(".")[1]!.replace(/-/g, "+").replace(/_/g, "/");
    const exp: unknown = JSON.parse(atob(body)).exp;
    if (typeof exp !== "number" || !Number.isSafeInteger(exp) || exp <= 0 || exp > Number.MAX_SAFE_INTEGER / 1000) return invalid();
    return exp * 1000;
  } catch { return invalid(); }
}
export function parseRunConfig(value: unknown): Omit<RunSession, "expire"> {
  if (!object(value) || typeof value.token !== "string" || typeof value.runId !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value.runId) || typeof value.readOnly !== "boolean") return invalid();
  const expiresAt = tokenExpiry(value.token);
  let symbols: SymbolRules[] | undefined;
  if (value.symbols !== undefined) {
    if (!Array.isArray(value.symbols) || value.symbols.length < 1 || value.symbols.length > 64) return invalid();
    symbols = value.symbols.map((s: unknown) => {
      if (!object(s) || typeof s.symbol !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$/.test(s.symbol) || typeof s.price_scale !== "number" || !Number.isInteger(s.price_scale) || s.price_scale < 0 || s.price_scale > 9 || typeof s.tick_size !== "string" || !/^[1-9][0-9]{0,18}$/.test(s.tick_size) || BigInt(s.tick_size) > 9223372036854775807n) return invalid();
      const rules: SymbolRules = { symbol: s.symbol, price_scale: s.price_scale, tick_size: s.tick_size };
      for (const key of ["lot_size", "min_quantity", "max_quantity"] as const) {
        const n = s[key];
        if (n !== undefined) {
          if (typeof n !== "number" || !Number.isInteger(n) || n < 1 || n > 2147483647) return invalid();
          rules[key] = n;
        }
      }
      return rules;
    });
    if (new Set(symbols.map(s => s.symbol)).size !== symbols.length) return invalid();
  }
  if (value.features !== undefined && (!object(value.features) || (value.features.demoControls !== undefined && typeof value.features.demoControls !== "boolean"))) return invalid();
  if (value.notifyTokenExpired !== undefined && typeof value.notifyTokenExpired !== "boolean") return invalid();
  return {
    gatewayUrl: endpoint(value.gatewayUrl, true, value.token), token: value.token,
    runId: value.runId, readOnly: value.readOnly, expiresAt, symbols,
    apiUrl: value.apiUrl === undefined ? undefined : endpoint(value.apiUrl, false, value.token),
    features: { demoControls: object(value.features) && value.features.demoControls === true },
    notifyTokenExpired: value.notifyTokenExpired === true,
    parentOrigin: value.parentOrigin === undefined ? undefined : exactOrigin(value.parentOrigin),
  };
}

export type EmbedState = { mode: "demo" | "waiting" | "invalid" } | { mode: "run"; run: Omit<RunSession, "expire"> };
/** One immutable session per document. Trust is configured in the child, never by messages/URLs. */
export function createEmbedding(win: Window) {
  let state: EmbedState = { mode: "demo" };
  let parentOrigin: string | undefined;
  let notified = false;
  let origins: string[] = [];
  const listeners = new Set<() => void>();
  const nonce = Array.from(win.crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, "0")).join("");
  try {
    const allowed = win.__FXP_CONSOLE_ALLOWED_ORIGINS__;
    if (allowed !== undefined) {
      if (!Array.isArray(allowed) || allowed.length > 16) return invalid();
      origins = allowed.map(exactOrigin);
    }
    if (win.__FXP_CONSOLE__ !== undefined) {
      const run = parseRunConfig(win.__FXP_CONSOLE__);
      if (run.notifyTokenExpired && (!run.parentOrigin || !origins.includes(run.parentOrigin))) return invalid();
      state = { mode: "run", run };
      parentOrigin = run.parentOrigin;
      delete win.__FXP_CONSOLE__;
    } else if (origins.length && win.parent !== win) state = { mode: "waiting" };
  } catch { state = { mode: "invalid" }; }
  const message = (event: MessageEvent) => {
    if (state.mode !== "waiting" || event.source !== win.parent || !origins.includes(event.origin)) return;
    const d: unknown = event.data;
    if (!object(d) || d.type !== "fxp-console:init" || d.version !== 1 || d.nonce !== nonce) return;
    try {
      const run = parseRunConfig(d.config);
      state = { mode: "run", run };
      parentOrigin = event.origin;
      for (const fn of listeners) fn();
    } catch { /* Invalid/untrusted messages never switch to demo or expose input. */ }
  };
  win.addEventListener("message", message);
  if (state.mode === "waiting") for (const origin of origins) win.parent.postMessage({ type: "fxp-console:ready", version: 1, nonce }, origin);
  return {
    getSnapshot: () => state,
    subscribe: (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn); }; },
    dispose: () => { win.removeEventListener("message", message); listeners.clear(); },
    notifyExpired: () => {
      if (notified || state.mode !== "run") return;
      notified = true;
      if (state.run.notifyTokenExpired && parentOrigin && win.parent !== win) win.parent.postMessage({ type: "fxp-console:token-expired", version: 1, runId: state.run.runId }, parentOrigin);
    },
  };
}

/** Fixed API paths only; never redirect credentials or fall back to demo HTTP. */
export async function consoleFetch(run: RunSession | undefined, path: string, init?: RequestInit): Promise<Response> {
  if (!run) return fetch(path, init);
  if (!run.apiUrl || !path.startsWith("/api/") || /^\/api\/(token|users)(?:[/?]|$)/.test(path) || path.includes("\\")) throw Error("Run HTTP API not configured for this operation");
  if (Date.now() >= run.expiresAt) { run.expire(); throw Error(RUN_ENDED); }
  const base = new URL(run.apiUrl.endsWith("/") ? run.apiUrl : run.apiUrl + "/");
  const url = new URL(path.slice(1), base);
  if (url.origin !== base.origin || !url.pathname.startsWith(base.pathname) || url.hash) throw Error("Invalid run API path");
  const headers = new Headers(init?.headers);
  headers.set("Authorization", `Bearer ${run.token}`);
  const res = await fetch(url.href, { ...init, headers, credentials: "omit", cache: "no-store", redirect: "error" });
  if (res.status === 401 || res.status === 403) { run.expire(); throw Error(RUN_ENDED); }
  if (!res.ok) throw Error(`Run HTTP request failed (${res.status})`);
  return res;
}

interface EventFeed {
  onopen: ((event: Event) => void) | null;
  onerror: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent<string>) => void) | null;
  close: () => void;
}
/** EventSource cannot set Authorization; opt-in run API streams use fetch. */
export function consoleEvents(run: RunSession | undefined, path: string): EventFeed {
  if (!run) return new EventSource(path);
  const abort = new AbortController();
  let retry: ReturnType<typeof setTimeout> | undefined;
  let lastId = "";
  const feed: EventFeed = { onopen: null, onerror: null, onmessage: null,
    close: () => { abort.abort(); clearTimeout(retry); } };
  const connect = async () => {
    try {
      const res = await consoleFetch(run, path, { signal: abort.signal, headers: lastId ? { "Last-Event-ID": lastId } : {} });
      if (!res.body || !res.headers.get("content-type")?.startsWith("text/event-stream")) throw Error();
      feed.onopen?.(new Event("open"));
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let pending = "";
      try {
        while (!abort.signal.aborted) {
          const chunk = await reader.read();
          if (chunk.done) break;
          pending = (pending + decoder.decode(chunk.value, { stream: true })).replace(/\r\n/g, "\n");
          if (pending.length > 65536) throw Error();
          let end: number;
          while ((end = pending.indexOf("\n\n")) >= 0) {
            const block = pending.slice(0, end); pending = pending.slice(end + 2);
            const lines = block.split("\n");
            const id = lines.find(l => l.startsWith("id:"));
            if (id && /^[A-Za-z0-9._:-]{1,128}$/.test(id.slice(3).trim())) lastId = id.slice(3).trim();
            const data = lines.filter(l => l.startsWith("data:")).map(l => l.slice(5).replace(/^ /, "")).join("\n");
            if (data) feed.onmessage?.(new MessageEvent("message", { data }));
          }
        }
      } finally { await reader.cancel().catch(() => {}); }
      throw Error();
    } catch (error) {
      if (abort.signal.aborted) return;
      feed.onerror?.(new Event("error"));
      if (!(error instanceof Error && error.message === RUN_ENDED) && Date.now() < run.expiresAt) retry = setTimeout(() => void connect(), 1000);
    }
  };
  void connect();
  return feed;
}
