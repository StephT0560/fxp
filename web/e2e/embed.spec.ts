// SPDX-License-Identifier: MIT
import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { decodeFrame } from "../src/lib/proto";

// Credentials are intentionally in browser memory/wire Logon, not test traces.
test.use({ trace: "off", video: "off" });
const example = new URL("../../examples/console-embed/", import.meta.url);
const frame = (page: Page) => page.frameLocator("#console");

async function seed(page: Page) {
  const auxiliary = await page.context().newPage();
  await auxiliary.goto("/");
  await auxiliary.getByTestId("login-alice").click();
  await expect(auxiliary.getByTestId("conn-state")).toContainText("live");
  await auxiliary.getByTestId("symbol-AAPL").click();
  await auxiliary.getByTestId("ticket-side-buy").click();
  await auxiliary.getByTestId("ticket-type").selectOption("2");
  await auxiliary.getByTestId("ticket-tif").selectOption("2");
  await auxiliary.getByTestId("ticket-qty").fill("10");
  await auxiliary.getByTestId("ticket-price").fill("1.00");
  await auxiliary.getByTestId("ticket-submit").click();
  await expect(auxiliary.getByTestId("ticket-result")).not.toContainText("pending");
  const id = (await auxiliary.getByTestId("ticket-result").innerText()).split(":")[0]!.trim();
  await expect(auxiliary.locator(`[data-order-id="${id}"]`)).toHaveAttribute("data-state", "new");
  await auxiliary.close();
  return id;
}
async function host(page: Page) {
  // Only static file hosting is adapted to the existing stack's origin. There
  // are no mocked market/token APIs or protocol responses: WS goes to the BFF.
  const consoleHtml = await (await page.request.get("/")).text();
  await page.route("**/__fxp_embed__/**", async route => {
    const filename = new URL(route.request().url()).pathname.split("/").at(-1)!;
    if (!["index.html", "host.js", "host.css"].includes(filename)) { await route.abort(); return; }
    await route.fulfill({ body: readFileSync(new URL(filename, example)), contentType: filename.endsWith("html") ? "text/html" : filename.endsWith("js") ? "text/javascript" : "text/css" });
  });
  await page.route("**/console.html", route => route.fulfill({ body: consoleHtml, contentType: "text/html" }));
  await page.goto("/__fxp_embed__/index.html");
  const response = await page.request.post("/api/token", { data: { user: "alice" } });
  expect(response.ok()).toBe(true);
  return (await response.json() as { token: string }).token;
}
async function enter(page: Page, token: string, mode = "injected", readOnly = false) {
  const origin = new URL(page.url()).origin;
  await page.locator("#gateway").fill(origin.replace(/^http/, "ws") + "/ws");
  await page.locator("#run-id").fill("embed-run");
  await page.locator("#token").fill(token);
  await page.locator("#delivery").selectOption(mode);
  await page.locator("#read-only").setChecked(readOnly);
  await page.getByRole("button", { name: "Connect run" }).click();
}

test("static injected four-field host shows real ladder, tape and recovered blotter without demo HTTP", async ({ page }) => {
  const id = await seed(page);
  const token = await host(page);
  const urls: string[] = [], logs: string[] = [];
  page.on("request", request => urls.push(request.url()));
  page.on("console", message => logs.push(message.text()));
  page.on("pageerror", error => logs.push(error.message));
  await enter(page, token);
  await expect(frame(page).getByTestId("conn-state")).toContainText("live", { timeout: 15000 });
  await expect(frame(page).getByTestId("run-id")).toHaveText("Run embed-run");
  await expect(frame(page).getByTestId("bid-row").first()).toBeVisible();
  await expect(frame(page).getByTestId("tape-row").first()).toBeVisible({ timeout: 30000 });
  await expect(frame(page).locator(`[data-order-id="${id}"]`)).toBeVisible();
  await expect(frame(page).getByTestId("sim-start")).toHaveCount(0);
  await expect(frame(page).getByRole("button", { name: "Scenarios", exact: true })).toHaveCount(0);
  await expect(frame(page).getByRole("button", { name: "Log out" })).toHaveCount(0);
  expect(urls.some(url => /\/api\//.test(url))).toBe(false);
  expect(urls.some(url => url.includes(token)) || logs.some(line => line.includes(token))).toBe(false);
  const storage = await frame(page).locator("body").evaluate(() => ({ local: Object.keys(localStorage), session: Object.keys(sessionStorage), injected: window.__FXP_CONSOLE__ }));
  expect(storage.local).toEqual([]); expect(storage.session).toEqual([]); expect(storage.injected).toBeUndefined();
});

test("read-only embed shows own orders but has no mutation controls or order frames", async ({ page }) => {
  await seed(page); const token = await host(page);
  const sent: string[] = [];
  page.on("websocket", socket => socket.on("framesent", event => { if (typeof event.payload !== "string") sent.push(decodeFrame(new Uint8Array(event.payload)).payload.case ?? "unknown"); }));
  await enter(page, token, "injected", true);
  await expect(frame(page).getByTestId("conn-state")).toContainText("live");
  await expect(frame(page).getByTestId("order-row").first()).toBeVisible();
  for (const control of ["ticket-submit", "order-cancel", "order-replace", "cancel-all"]) await expect(frame(page).getByTestId(control)).toHaveCount(0);
  expect(sent.includes("logon") && sent.includes("orderStatusRequest")).toBe(true);
  expect(sent.some(kind => ["newOrder", "orderCancel", "orderCancelReplace", "orderMassCancel"].includes(kind))).toBe(false);
});

test("ignores foreign-source/origin init, then accepts the allowed parent nonce handshake", async ({ page }) => {
  const token = await host(page);
  await page.evaluate(() => {
    window.addEventListener("message", event => {
      if (event.data?.type === "fxp-console:ready") {
        event.stopImmediatePropagation();
        (window as unknown as { readyNonce: string }).readyNonce = event.data.nonce;
      }
    }, { capture: true });
  });
  await enter(page, token, "message");
  await expect(frame(page).getByRole("status")).toContainText("Waiting for run configuration");
  await expect.poll(() => page.evaluate(() => Boolean((window as unknown as { readyNonce?: string }).readyNonce))).toBe(true);
  await page.evaluate(({ token, origin }) => {
    const nonce = (window as unknown as { readyNonce: string }).readyNonce;
    const config = { gatewayUrl: origin.replace(/^http/, "ws") + "/ws", token, runId: "embed-run", readOnly: true };
    const payload = { type: "fxp-console:init", version: 1, nonce, config };
    const hostile = document.createElement("iframe");
    hostile.sandbox.add("allow-scripts");
    // A foreign opaque-origin sibling has the valid nonce/config, but not the
    // allowed parent's source or origin. Only test-controlled JSON is embedded.
    hostile.srcdoc = `<script>parent.frames[0].postMessage(${JSON.stringify(payload).replace(/</g, "\\u003c")}, ${JSON.stringify(origin)})<\/script>`;
    document.body.append(hostile);
  }, { token, origin: new URL(page.url()).origin });
  await page.waitForTimeout(100);
  await expect(frame(page).getByRole("status")).toContainText("Waiting for run configuration");
  await page.evaluate(({ token, origin }) => {
    const child = (document.querySelector("#console") as HTMLIFrameElement).contentWindow!;
    child.postMessage({ type: "fxp-console:init", version: 1, nonce: (window as unknown as { readyNonce: string }).readyNonce, config: { gatewayUrl: origin.replace(/^http/, "ws") + "/ws", token, runId: "embed-run", readOnly: true } }, origin);
  }, { token, origin: new URL(page.url()).origin });
  await expect(frame(page).getByTestId("conn-state")).toContainText("live");
});

test("token expiration is terminal and notifies only the allowed host without a token payload", async ({ page }) => {
  const token = await host(page); await page.clock.install();
  await page.locator("#notify").check();
  const callbackMessages: unknown[] = [];
  await page.exposeFunction("expiredEvent", (data: unknown) => { callbackMessages.push(data); });
  await page.evaluate(() => window.addEventListener("message", event => {
    if (event.data?.type === "fxp-console:token-expired") void (window as unknown as { expiredEvent: (data: unknown) => Promise<void> }).expiredEvent(event.data);
  }));
  await enter(page, token);
  await expect(frame(page).getByTestId("conn-state")).toContainText("live");
  await page.clock.fastForward(16 * 60 * 1000);
  await expect(frame(page).getByTestId("conn-error")).toHaveText("Run ended or token expired");
  await expect(page.locator("#host-status")).toContainText("ended or token expired");
  expect(callbackMessages).toEqual([{ type: "fxp-console:token-expired", version: 1, runId: "embed-run" }]);
  expect(JSON.stringify(callbackMessages).includes(token)).toBe(false);
  await expect(frame(page).getByTestId("ticket-submit")).toHaveCount(0);
});
