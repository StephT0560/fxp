// SPDX-License-Identifier: MIT
import { RUN_ENDED, type RunSession } from "./run-config";
import { captureFrame } from "./inspector";
import type { ExecutionReport, FXPMessage } from "../gen/fxp_pb";
import { newOrderId, trackCancel, trackNew, trackReplace, type OrderRow } from "./orders";
import {
  cancelFrame,
  decodeFrame,
  logonFrame,
  massCancelFrame,
  newOrderFrame,
  replaceFrame,
  statusRequestFrame,
  subscribeFrame,
  type ReplaceChange,
} from "./proto";
import { useBook, useOrders, useSession, useTape } from "./stores";
import type { OrderDraft } from "./validate";

export interface ClientOptions {
  user: string;
  run?: Omit<RunSession, "expire">;
  onTokenExpired?: () => void;
  symbols: () => string[];
  wsUrl?: string;
  fetchToken?: (user: string) => Promise<string>;
  WebSocketImpl?: typeof WebSocket;
  random?: () => number;
  resubscribeMs?: number;
}

export const BACKOFF_BASE_MS = 500;
export const BACKOFF_MAX_MS = 10_000;

/** Exponential backoff with up to 250 ms of jitter. */
export function backoffMs(attempt: number, random: () => number = Math.random): number {
  return Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** attempt) + Math.floor(random() * 250);
}

export async function fetchToken(user: string): Promise<string> {
  const res = await fetch("/api/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ user }),
  });
  if (!res.ok) throw new Error(`token: ${res.status} ${(await res.text()).trim()}`);
  const body = (await res.json()) as { token?: unknown };
  if (typeof body.token !== "string") throw new Error("token: malformed response");
  return body.token;
}

function sameOriginWs(): string {
  return `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/ws`;
}

export function dispatch(msg: FXPMessage): void {
  const p = msg.payload;
  switch (p.case) {
    case "marketDataSnapshot":
      useBook.getState().onSnapshot(p.value);
      break;
    case "marketDataIncremental":
      useBook.getState().onIncremental(p.value);
      break;
    case "tradeCaptureReport":
      useTape.getState().onTrade(p.value);
      break;
    case "tradingSessionStatus":
      useSession.getState().onStatus(p.value.symbol, p.value.phase);
      break;
    case "auctionInfo":
      useSession.getState().onAuction(p.value);
      break;
    case "executionReport":
      useOrders.getState().onReport(p.value);
      break;
    case "orderMassCancelReport":
      useOrders.getState().onMassCancel(p.value);
      break;
    case "errorMessage":
      useOrders.getState().note("reject", `gateway error: ${p.value.errorMessage}`);
      break;
    default:
      break;
  }
}

/**
 * Same-origin WebSocket session: fetches a JWT (memory only), sends Logon as
 * the first frame, subscribes once LogonAck is accepted, re-subscribes any
 * symbol still lacking a snapshot, and reconnects with backoff.
 */
export class ConsoleClient {
  private ws?: WebSocket;
  private attempt = 0;
  private stopped = false;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private resubTimer?: ReturnType<typeof setInterval>;
  private readonly opts: Required<Omit<ClientOptions, "run" | "onTokenExpired">> & Pick<ClientOptions, "run" | "onTokenExpired">;
  private expiryTimer?: ReturnType<typeof setTimeout>;
  private expired = false;
  private loggedOn = false;
  private statusRequest?: string;
  private statusReports: ExecutionReport[] = [];

  constructor(opts: ClientOptions) {
    this.opts = {
      WebSocketImpl: WebSocket,
      random: Math.random,
      resubscribeMs: 2000,
      ...opts,
      wsUrl: opts.run?.gatewayUrl ?? opts.wsUrl ?? sameOriginWs(),
      fetchToken: opts.run ? async () => opts.run!.token : (opts.fetchToken ?? fetchToken),
    };
  }

  start(): void {
    if (this.expired) return;
    this.stopped = false;
    if (this.opts.run) this.armExpiry();
    if (this.expired) return;
    void this.connect();
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    clearTimeout(this.expiryTimer);
    clearInterval(this.resubTimer);
    this.ws?.close();
    this.ws = undefined;
    this.loggedOn = false;
    if (!this.expired) useSession.getState().setConn("idle");
  }

  /** A terminal state: never fetch a demo token or reconnect with expired credentials. */
  expire(): void {
    if (this.expired || !this.opts.run) return;
    this.expired = true;
    this.stop();
    useBook.getState().unsync();
    useOrders.getState().markStale();
    useSession.getState().setConn("ended", RUN_ENDED);
    this.opts.onTokenExpired?.();
  }

  private armExpiry(): void {
    const remaining = this.opts.run!.expiresAt - Date.now();
    if (remaining <= 0) { this.expire(); return; }
    this.expiryTimer = setTimeout(() => this.armExpiry(), Math.min(remaining, 2147483647));
  }

  private async connect(): Promise<void> {
    if (this.stopped) return;
    if (this.opts.run && Date.now() >= this.opts.run.expiresAt) { this.expire(); return; }
    const session = useSession.getState();
    session.setConn(this.attempt === 0 ? "connecting" : "reconnecting", session.error, this.attempt);
    let token: string;
    try {
      token = await this.opts.fetchToken(this.opts.user);
    } catch (e) {
      this.retry(this.opts.run ? "Run connection interrupted" : String(e instanceof Error ? e.message : e));
      return;
    }
    if (this.stopped) return;
    let ws: WebSocket;
    try { ws = new this.opts.WebSocketImpl(this.opts.wsUrl); }
    catch { this.retry("Connection unavailable"); return; }
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    let loggedOn = false;
    ws.onopen = () => {
      if (this.stopped || this.ws !== ws) return;
      if (this.opts.run && Date.now() >= this.opts.run.expiresAt) { this.expire(); return; }
      const frame = logonFrame(token, `console-${this.opts.user}`);
      ws.send(frame);
      captureFrame(frame, "sent");
      token = "";
    };
    ws.onmessage = (ev: MessageEvent) => {
      if (this.stopped || this.ws !== ws) return;
      if (!(ev.data instanceof ArrayBuffer)) return;
      captureFrame(ev.data, "received");
      let msg: FXPMessage;
      try {
        msg = decodeFrame(ev.data);
      } catch {
        return;
      }
      if (msg.payload.case === "logonAck") {
        if (!msg.payload.value.accepted) {
          if (this.opts.run) { this.expire(); return; }
          this.stopped = true;
          useSession.getState().setConn("rejected", msg.payload.value.rejectReason || "logon rejected");
          ws.close();
          return;
        }
        loggedOn = true;
        this.loggedOn = true;
        this.attempt = 0;
        useSession.getState().setConn("live");
        this.subscribe(ws, this.opts.symbols());
        // Rebuild the blotter from the core rather than trusting local state.
        this.requestStatus();
        clearInterval(this.resubTimer);
        this.resubTimer = setInterval(() => this.resubscribeUnsynced(ws), this.opts.resubscribeMs);
        return;
      }
      if (!loggedOn) return;
      if (msg.payload.case === "orderStatusResponse") this.onStatusResponse(msg.payload.value);
      else dispatch(msg);
    };
    ws.onclose = (ev: CloseEvent) => {
      if (this.stopped || this.ws !== ws) return;
      clearInterval(this.resubTimer);
      this.loggedOn = false;
      useBook.getState().unsync();
      useOrders.getState().markStale();
      if (this.opts.run && [1000, 1008, 4401, 4403].includes(ev.code)) { this.expire(); return; }
      this.retry(this.opts.run ? "Run connection interrupted" : (ev.reason || `connection closed (${ev.code})`));
    };
  }

  get ready(): boolean {
    return !this.expired && (!this.opts.run || Date.now() < this.opts.run.expiresAt) && this.loggedOn && this.ws !== undefined && this.ws.readyState === this.ws.OPEN;
  }

  private send(frame: Uint8Array): boolean {
    if (!this.ready) return false;
    this.ws!.send(frame);
    captureFrame(frame, "sent");
    return true;
  }

  /** Sends a validated order; returns its client order id, or undefined when offline. */
  placeOrder(d: OrderDraft): string | undefined {
    if (!this.ready || this.opts.run?.readOnly) return undefined;
    const id = newOrderId();
    trackNew(useOrders.getState().blotter, id, d);
    useOrders.getState().touch();
    this.send(newOrderFrame(id, d));
    return id;
  }

  cancel(row: OrderRow): boolean {
    if (!this.ready || this.opts.run?.readOnly) return false;
    trackCancel(useOrders.getState().blotter, row.orderId);
    useOrders.getState().touch();
    return this.send(cancelFrame(row.orderId, row.symbol, newOrderId()));
  }

  replace(row: OrderRow, change: ReplaceChange): string | undefined {
    if (!this.ready || this.opts.run?.readOnly) return undefined;
    const id = newOrderId();
    trackReplace(useOrders.getState().blotter, row.orderId, id, change);
    useOrders.getState().touch();
    this.send(replaceFrame(row.orderId, id, row.symbol, change));
    return id;
  }

  /** Mass cancel of this user's orders, all symbols or one. */
  cancelAll(symbol?: string): boolean {
    if (!this.ready || this.opts.run?.readOnly) return false;
    useOrders.getState().note("request", `cancel-all (${symbol ?? "all symbols"})`);
    return this.send(massCancelFrame(newOrderId(), symbol));
  }

  requestStatus(): void {
    this.statusRequest = newOrderId();
    this.statusReports = [];
    this.send(statusRequestFrame(this.statusRequest));
  }

  private onStatusResponse(r: { requestId: string; reports: ExecutionReport[]; complete: boolean; rejectReason: string }): void {
    if (r.requestId !== this.statusRequest) return;
    if (r.rejectReason) {
      this.statusRequest = undefined;
      useOrders.getState().note("reject", `order status rejected — ${r.rejectReason}`);
      return;
    }
    this.statusReports.push(...r.reports);
    if (!r.complete) return;
    this.statusRequest = undefined;
    useOrders.getState().onStatus(this.statusReports);
    this.statusReports = [];
  }

  private subscribe(ws: WebSocket, symbols: string[]): void {
    if (symbols.length > 0 && ws.readyState === ws.OPEN) {
      const frame = subscribeFrame(symbols);
      ws.send(frame);
      captureFrame(frame, "sent");
    }
  }

  // The core ignores requests for books it has not created yet, so keep
  // asking until each symbol has a snapshot.
  private resubscribeUnsynced(ws: WebSocket): void {
    const books = useBook.getState().books;
    this.subscribe(ws, this.opts.symbols().filter((s) => !books.get(s)?.synced));
  }

  private retry(error: string): void {
    if (this.stopped) return;
    this.ws = undefined;
    const delay = backoffMs(this.attempt, this.opts.random);
    this.attempt++;
    useSession.getState().setConn("reconnecting", error, this.attempt);
    this.retryTimer = setTimeout(() => void this.connect(), delay);
  }
}

/** The signed-in session's client, for components that send orders. */
export const activeClient: { current?: ConsoleClient } = {};
