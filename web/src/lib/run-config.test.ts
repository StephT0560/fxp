// SPDX-License-Identifier: MIT
import { afterEach, describe, expect, it, vi } from "vitest";
import { consoleEvents, consoleFetch, createEmbedding, parseRunConfig, RUN_ENDED, type RunSession } from "./run-config";

export function fixtureToken(exp = Math.floor(Date.now() / 1000) + 3600): string {
  return [btoa('{"alg":"HS256"}'), btoa(JSON.stringify({ exp, sub: "fixture" })), "fixture"].map(v => v.replace(/=/g, "").replace(/\+/g, "-").replace(/\//g, "_")).join(".");
}
const config = () => ({ gatewayUrl: "ws://127.0.0.1:8081/ws", token: fixtureToken(), runId: "run-1", readOnly: false });
afterEach(() => { delete window.__FXP_CONSOLE__; delete window.__FXP_CONSOLE_ALLOWED_ORIGINS__; vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("run configuration boundary", () => {
  it("falls back to demo only when no embedding config exists, and consumes a valid injected config", () => {
    const demo = createEmbedding(window); expect(demo.getSnapshot()).toEqual({ mode: "demo" }); demo.dispose();
    window.__FXP_CONSOLE__ = config();
    const injected = createEmbedding(window);
    expect(injected.getSnapshot().mode).toBe("run");
    expect(window.__FXP_CONSOLE__).toBeUndefined(); injected.dispose();
  });
  it("fails closed on invalid injection without reflecting secrets", () => {
    for (const patch of [{ gatewayUrl: "wss://host/ws?token=secret" }, { gatewayUrl: "ws://external.example/ws" }, { apiUrl: "https://user:password@host/" }, { readOnly: undefined }, { token: "invalid-private-token" }, { symbols: [{ symbol: "TEST", price_scale: 2, tick_size: "0" }] }]) {
      window.__FXP_CONSOLE__ = { ...config(), ...patch };
      const embed = createEmbedding(window); expect(embed.getSnapshot()).toEqual({ mode: "invalid" }); embed.dispose();
    }
  });
  it("requires exact origin, parent source, nonce and version; locks the first session; callback is opt-in and targeted", () => {
    const parent = { postMessage: vi.fn() };
    vi.spyOn(window, "parent", "get").mockReturnValue(parent as unknown as Window);
    window.__FXP_CONSOLE_ALLOWED_ORIGINS__ = ["https://host.example"];
    const embed = createEmbedding(window);
    expect(embed.getSnapshot().mode).toBe("waiting");
    const ready = parent.postMessage.mock.calls[0]![0] as { nonce: string };
    const data = { type: "fxp-console:init", version: 1, nonce: ready.nonce, config: { ...config(), notifyTokenExpired: true } };
    const send = (origin: string, source: MessageEventSource | null, message = data) => window.dispatchEvent(new MessageEvent("message", { origin, source, data: message }));
    send("https://evil.example", parent as unknown as Window);
    send("https://host.example.evil.test", parent as unknown as Window);
    send("null", parent as unknown as Window);
    send("https://host.example", window);
    send("https://host.example", parent as unknown as Window, { ...data, nonce: "wrong" });
    send("https://host.example", parent as unknown as Window, { ...data, version: 2 });
    expect(embed.getSnapshot().mode).toBe("waiting");
    send("https://host.example", parent as unknown as Window);
    const first = embed.getSnapshot(); expect(first.mode).toBe("run");
    send("https://host.example", parent as unknown as Window, { ...data, config: { ...data.config, runId: "replacement" } });
    expect(embed.getSnapshot()).toBe(first);
    embed.notifyExpired(); embed.notifyExpired();
    expect(parent.postMessage).toHaveBeenLastCalledWith({ type: "fxp-console:token-expired", version: 1, runId: "run-1" }, "https://host.example");
    expect(JSON.stringify(parent.postMessage.mock.calls)).not.toContain(data.config.token);
    expect(parent.postMessage).toHaveBeenCalledTimes(2); embed.dispose();
  });
  it("rejects wildcard/opaque/path origins instead of trusting message-provided lists", () => {
    for (const origin of ["*", "null", "https://host.example/", "https://host.example/path"]) {
      window.__FXP_CONSOLE_ALLOWED_ORIGINS__ = [origin];
      const embed = createEmbedding(window); expect(embed.getSnapshot().mode).toBe("invalid"); embed.dispose();
    }
  });
});

describe("authenticated run HTTP", () => {
  it("uses only the configured base, Bearer header, no credentials/redirects, and never fetches demo tokens", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("{}")); vi.stubGlobal("fetch", fetch);
    const run: RunSession = { ...parseRunConfig({ ...config(), apiUrl: "https://api.example/runs/run-1/" }), expire: vi.fn() };
    await consoleFetch(run, "/api/symbols");
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("https://api.example/runs/run-1/api/symbols");
    expect(new Headers(init.headers).get("Authorization")).toBe(`Bearer ${run.token}`);
    expect(init).toMatchObject({ credentials: "omit", redirect: "error", cache: "no-store" });
    for (const path of ["/api/token", "/api/users", "//evil.example/api/x", "/api/../../outside", "/api/\\evil"]) await expect(consoleFetch(run, path)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("ends on HTTP auth failure and never displays the response body", async () => {
    const run: RunSession = { ...parseRunConfig({ ...config(), apiUrl: "https://api.example/" }), expire: vi.fn() };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(run.token, { status: 401 })));
    await expect(consoleFetch(run, "/api/symbols")).rejects.toThrow(RUN_ENDED);
    expect(run.expire).toHaveBeenCalledTimes(1);
  });
  it("passes the same Bearer header to bounded SSE streams without a query token", async () => {
    const run: RunSession = { ...parseRunConfig({ ...config(), apiUrl: "https://api.example/" }), expire: vi.fn() };
    const fetch = vi.fn().mockResolvedValue(new Response('id: 1\r\ndata: {"seq":1}\r\n\r\n', { headers: { "Content-Type": "text/event-stream" } }));
    vi.stubGlobal("fetch", fetch);
    const feed = consoleEvents(run, "/api/scenarios/test/events");
    const received: string[] = [];
    feed.onmessage = event => { received.push(event.data); feed.close(); };
    await vi.waitFor(() => expect(received).toEqual(['{"seq":1}']));
    expect(new Headers(fetch.mock.calls[0]![1].headers).get("Authorization")).toBe(`Bearer ${run.token}`);
    expect(fetch.mock.calls[0]![0]).not.toContain(run.token);
    feed.close();
  });
});
