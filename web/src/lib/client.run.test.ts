// SPDX-License-Identifier: MIT
import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FXPMessageSchema, OrderSide, OrderType, TimeInForce, SelfTradePrevention } from "../gen/fxp_pb";
import { ConsoleClient } from "./client";
import { decodeFrame } from "./proto";
import { consoleFetch, parseRunConfig, RUN_ENDED } from "./run-config";
import { useOrders, useSession } from "./stores";
import type { OrderDraft } from "./validate";

class Socket {
  static all: Socket[] = [];
  readonly OPEN = 1;
  readyState = 1;
  binaryType = "";
  sent: Uint8Array[] = [];
  onopen?: () => void;
  onmessage?: (event: MessageEvent) => void;
  onclose?: (event: CloseEvent) => void;
  constructor(readonly url: string) { Socket.all.push(this); }
  send(frame: Uint8Array) { this.sent.push(frame); }
  close() { this.readyState = 3; this.onclose?.(new CloseEvent("close", { code: 1000 })); }
  ack(accepted = true) {
    const data = toBinary(FXPMessageSchema, create(FXPMessageSchema, { payload: { case: "logonAck", value: { accepted } } }));
    this.onmessage?.(new MessageEvent("message", { data: data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) }));
  }
}
const clients: ConsoleClient[] = [];
const token = () => `${btoa('{"alg":"HS256"}').replace(/=/g, "")}.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 10 })).replace(/=/g, "")}.fixture`;
const draft: OrderDraft = { symbol: "AAPL", side: OrderSide.BUY, type: OrderType.LIMIT, tif: TimeInForce.GTC, price: 100n, stopPrice: 0n, qty: 1, displayQty: 0, expireDate: "", stpMode: SelfTradePrevention.UNSPECIFIED, stpGroup: "" };
async function connect(readOnly: boolean, callback = vi.fn()) {
  const run = parseRunConfig({ gatewayUrl: "ws://127.0.0.1:8081/ws", runId: "run-test", token: token(), readOnly });
  const forbiddenFetch = vi.fn().mockRejectedValue(Error("demo tokens forbidden"));
  const client = new ConsoleClient({ user: "run", run, onTokenExpired: callback, fetchToken: forbiddenFetch, symbols: () => ["AAPL"], WebSocketImpl: Socket as unknown as typeof WebSocket, resubscribeMs: 60000 });
  clients.push(client); client.start(); await Promise.resolve(); await Promise.resolve();
  const socket = Socket.all.at(-1)!; socket.onopen?.(); socket.ack();
  return { client, socket, callback, forbiddenFetch, run };
}
afterEach(() => { for (const client of clients.splice(0)) client.stop(); Socket.all = []; vi.useRealTimers(); vi.unstubAllGlobals(); useOrders.getState().reset(); useSession.getState().setConn("idle"); });

describe("run-scoped transport", () => {
  it("uses only supplied URL/token and blocks every read-only mutation before optimistic state or frames", async () => {
    const { client, socket, forbiddenFetch, run } = await connect(true);
    expect(client.ready).toBe(true); expect(socket.url).toBe(run.gatewayUrl);
    expect(decodeFrame(socket.sent[0]!).payload.value).toMatchObject({ token: run.token });
    const before = socket.sent.length;
    const fake = { orderId: "working", symbol: "AAPL" } as Parameters<ConsoleClient["cancel"]>[0];
    expect(client.placeOrder(draft)).toBeUndefined(); expect(client.cancel(fake)).toBe(false);
    expect(client.replace(fake, { price: 101n })).toBeUndefined(); expect(client.cancelAll()).toBe(false);
    expect(socket.sent).toHaveLength(before); expect(useOrders.getState().blotter.rows.size).toBe(0);
    expect(socket.sent.map(f => decodeFrame(f).payload.case)).toEqual(["logon", "marketDataRequest", "orderStatusRequest"]);
    expect(forbiddenFetch).not.toHaveBeenCalled();
  });
  it.each(["close", "rejection"])("blocks run HTTP after terminal %s even before JWT expiry", async reason => {
    const { client, socket, run } = await connect(false);
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    if (reason === "close") socket.onclose?.(new CloseEvent("close", { code: 1000 }));
    else socket.ack(false);
    expect(Date.now()).toBeLessThan(run.expiresAt);
    expect(useSession.getState().conn).toBe("ended");
    const session = { ...run, apiUrl: "http://127.0.0.1:3000/", expire: () => client.expire() };
    for (const path of ["/api/sim/start", "/api/scenarios/test/run"]) {
      await expect(consoleFetch(session, path, { method: "POST" })).rejects.toThrow(RUN_ENDED);
    }
    expect(fetch).not.toHaveBeenCalled();
    // Demo HTTP still uses the existing path even when a run has ended.
    await consoleFetch(undefined, "/api/sim/start", { method: "POST" });
    expect(fetch).toHaveBeenCalledOnce();
  });
  it("allows authenticated run HTTP during a transient reconnect", async () => {
    const { client, socket, run } = await connect(false);
    socket.onclose?.(new CloseEvent("close", { code: 1006 }));
    expect(useSession.getState().conn).toBe("reconnecting");
    const fetch = vi.fn().mockResolvedValue(new Response("{}")); vi.stubGlobal("fetch", fetch);
    await consoleFetch({ ...run, apiUrl: "http://127.0.0.1:3000/", expire: () => client.expire() }, "/api/sim/start", { method: "POST" });
    expect(fetch).toHaveBeenCalledOnce();
  });
  it("retains trading for writable runs", async () => {
    const { client, socket } = await connect(false);
    expect(client.placeOrder(draft)).toBeTruthy();
    expect(decodeFrame(socket.sent.at(-1)!).payload.case).toBe("newOrder");
  });
  it("ends at exp, closes once and does not reconnect or leak the credential into session errors", async () => {
    vi.useFakeTimers(); const { client, socket, callback, run } = await connect(false);
    await vi.advanceTimersByTimeAsync(11000);
    expect(client.ready).toBe(false); expect(socket.readyState).toBe(3); expect(callback).toHaveBeenCalledTimes(1);
    expect(useSession.getState().conn).toBe("ended"); expect(useSession.getState().error).toBe(RUN_ENDED);
    expect(JSON.stringify(useSession.getState())).not.toContain(run.token);
    await vi.advanceTimersByTimeAsync(60000); expect(Socket.all).toHaveLength(1);
  });
  it("ends on rejected Logon without reflecting the server reason, but normal disposal sends no expiry callback", async () => {
    const { client, socket, callback } = await connect(false); socket.ack(false);
    expect(client.ready).toBe(false); expect(callback).toHaveBeenCalledTimes(1); expect(useSession.getState().error).toBe(RUN_ENDED);
    const second = await connect(false); second.client.stop(); expect(second.callback).not.toHaveBeenCalled();
  });
});
it('notifies a run host of disconnects so it can acquire a fresh credential',async()=>{
 const onSessionLost=vi.fn();
 const run=parseRunConfig({gatewayUrl:'ws://127.0.0.1:8081/ws',runId:'run-test',token:token(),readOnly:false,notifySessionLost:true});
 const client=new ConsoleClient({user:'run',run,onSessionLost,symbols:()=>['AAPL'],WebSocketImpl:Socket as unknown as typeof WebSocket});
 clients.push(client);client.start();await Promise.resolve();await Promise.resolve();
 const socket=Socket.all.at(-1)!;socket.onopen?.();socket.ack();socket.onclose?.(new CloseEvent('close',{code:1006}));
 expect(onSessionLost).toHaveBeenCalledWith('disconnected');expect(client.ready).toBe(false);
});
