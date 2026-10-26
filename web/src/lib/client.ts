// SPDX-License-Identifier: MIT
import type { FXPMessage } from "../gen/fxp_pb";
import { decodeFrame, logonFrame, subscribeFrame } from "./proto";
import { useBook, useSession, useTape } from "./stores";

export interface ClientOptions {
  user: string;
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
  private readonly opts: Required<ClientOptions>;

  constructor(opts: ClientOptions) {
    this.opts = {
      wsUrl: sameOriginWs(),
      fetchToken,
      WebSocketImpl: WebSocket,
      random: Math.random,
      resubscribeMs: 2000,
      ...opts,
    };
  }

  start(): void {
    this.stopped = false;
    void this.connect();
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    clearInterval(this.resubTimer);
    this.ws?.close();
    this.ws = undefined;
    useSession.getState().setConn("idle");
  }

  private async connect(): Promise<void> {
    const session = useSession.getState();
    session.setConn(this.attempt === 0 ? "connecting" : "reconnecting", session.error, this.attempt);
    let token: string;
    try {
      token = await this.opts.fetchToken(this.opts.user);
    } catch (e) {
      this.retry(String(e instanceof Error ? e.message : e));
      return;
    }
    if (this.stopped) return;
    const ws = new this.opts.WebSocketImpl(this.opts.wsUrl);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    let loggedOn = false;
    ws.onopen = () => ws.send(logonFrame(token, `console-${this.opts.user}`));
    ws.onmessage = (ev: MessageEvent) => {
      if (!(ev.data instanceof ArrayBuffer)) return;
      let msg: FXPMessage;
      try {
        msg = decodeFrame(ev.data);
      } catch {
        return;
      }
      if (msg.payload.case === "logonAck") {
        if (!msg.payload.value.accepted) {
          this.stopped = true;
          useSession.getState().setConn("rejected", msg.payload.value.rejectReason || "logon rejected");
          ws.close();
          return;
        }
        loggedOn = true;
        this.attempt = 0;
        useSession.getState().setConn("live");
        this.subscribe(ws, this.opts.symbols());
        clearInterval(this.resubTimer);
        this.resubTimer = setInterval(() => this.resubscribeUnsynced(ws), this.opts.resubscribeMs);
        return;
      }
      if (loggedOn) dispatch(msg);
    };
    ws.onclose = (ev: CloseEvent) => {
      if (this.ws !== ws) return;
      clearInterval(this.resubTimer);
      useBook.getState().unsync();
      this.retry(ev.reason || `connection closed (${ev.code})`);
    };
  }

  private subscribe(ws: WebSocket, symbols: string[]): void {
    if (symbols.length > 0 && ws.readyState === ws.OPEN) ws.send(subscribeFrame(symbols));
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
