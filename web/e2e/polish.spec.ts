// SPDX-License-Identifier: MIT
import { expect, test, type Page } from "@playwright/test";
async function login(page: Page) {
  await page.goto("/");
  await page.getByTestId("login-alice").click();
  await expect(page.getByTestId("conn-state")).toContainText("live");
}
test("panel tabs have a visible active state and keyboard focus", async ({ page }) => {
  await login(page);
  const market = page.getByRole("button", { name: "Market", exact: true });
  const protocol = page.getByRole("button", { name: "Protocol inspector", exact: true });
  const active = await market.evaluate((el) => getComputedStyle(el).backgroundColor);
  await protocol.click();
  await expect(protocol).toHaveAttribute("aria-pressed", "true");
  expect(await protocol.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe(active);
  expect(await market.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe(active);
  await market.focus(); await page.keyboard.press("Enter");
  await expect(market).toBeFocused();
  await expect(market).toHaveAttribute("aria-pressed", "true");
  expect(await market.evaluate((el) => getComputedStyle(el).boxShadow)).not.toBe("none");
});

test("a resting limit marks its own ladder level until canceled", async ({ page }) => {
  await login(page);
  await page.getByTestId("symbol-DEMO").click();
  await page.getByTestId("ticket-qty").fill("7");
  await page.getByTestId("ticket-price").fill("31.23");
  await page.getByTestId("ticket-submit").click();
  const level = page.getByTestId("ladder").getByTestId("bid-row").filter({ hasText: "31.23" });
  await expect(level.getByTestId("own-order-marker")).toContainText("7");
  const row = page.getByTestId("order-row").filter({ hasText: "31.23" });
  await row.getByTestId("order-cancel").click();
  await expect(level.getByTestId("own-order-marker")).toHaveCount(0);
});

test("simulator call counts down without inventing a scenario deadline", async ({ page }) => {
  await page.request.post("/api/sim/stop");
  await login(page);
  await page.getByTestId("symbol-DEMO").click();
  await page.getByTestId("sim-auction-secs").fill("10");
  await page.getByTestId("sim-start").click();
  const countdown = page.getByTestId("auction-countdown");
  await expect(countdown).toContainText(/uncross in \d+ s/);
  const before = await countdown.innerText();
  await expect(countdown).not.toHaveText(before, { timeout: 2500 });
  await page.request.post("/api/sim/stop");
  await expect(countdown).toBeHidden({ timeout: 15000 });
});

test("the compact ticket keeps advanced controls disclosed and GTD expiry required", async ({ page }) => {
  await page.setViewportSize({ width: 1366, height: 768 }); await login(page);
  await expect(page.getByTestId("ticket-display")).toBeHidden();
  await expect(page.getByTestId("ticket-stp")).toBeHidden();
  expect(await page.getByTestId("ticket").evaluate((el) => el.scrollHeight <= el.clientHeight)).toBe(true);
  await page.getByTestId("ticket-advanced").click();
  await expect(page.getByTestId("ticket-display")).toBeVisible();
  await expect(page.getByTestId("ticket-stp-group")).toBeVisible();
  await page.getByTestId("ticket-advanced").click();
  await page.getByTestId("ticket-tif").selectOption("7");
  await expect(page.getByTestId("ticket-expiry")).toBeVisible();
});

test("Quick demo uses alice, waits for live and shares simulator defaults", async ({ page }) => {
  await page.request.post("/api/sim/stop");
  await page.goto("/");
  await expect(page.getByTestId("login-market-maker")).toHaveCount(0);
  const started = page.waitForRequest((r) => r.url().endsWith("/api/sim/start") && r.method() === "POST");
  await page.getByTestId("quick-demo").click();
  const request = await started;
  expect(request.postDataJSON()).toEqual({ seed: 42, symbols: 10, participants: 20, duration: 120, opening_auction_sec: 10 });
  await expect(page.getByTestId("conn-state")).toContainText("live");
  await expect(page.getByTestId("phase")).toContainText("AAPL");
  await expect(page.getByTestId("tape-row").first()).toBeVisible({ timeout: 30000 });
  await page.getByRole("button", { name: "Log out" }).click();
  const duplicate = page.waitForResponse((r) => r.url().endsWith("/api/sim/start") && r.request().method() === "POST");
  await page.getByTestId("quick-demo").click();
  expect((await duplicate).status()).toBe(409);
  await expect(page.getByTestId("conn-state")).toContainText("live");
  await expect(page.getByText(/Quick demo:/)).toHaveCount(0);
  await page.request.post("/api/sim/stop");
});

test("an uninitialized market explains how to get data", async ({ page }) => {
  await page.routeWebSocket(/\/ws$/, () => { /* Deliberately no snapshot or print. */ });
  await page.goto("/"); await page.getByTestId("login-alice").click();
  for (const panel of ["ladder", "tape"]) await expect(page.getByTestId(panel)).toContainText("No market data yet. Start the simulator or use Quick demo.");
});

test("keyboard ladder selection prefills a scaled price and focuses quantity", async ({ page }) => {
  await login(page); await page.getByTestId("symbol-DEMO").click();
  await page.getByTestId("ticket-qty").fill("1"); await page.getByTestId("ticket-price").fill("28.37");
  await page.getByTestId("ticket-submit").click();
  const level = page.getByTestId("ladder").getByTestId("bid-row").filter({ hasText: "28.37" });
  await expect(level).toBeVisible(); await page.getByTestId("ticket-price").fill("29.00");
  await level.focus(); await page.keyboard.press("Enter");
  await expect(page.getByTestId("ticket-price")).toHaveValue("28.37");
  await expect(page.getByTestId("ticket-qty")).toBeFocused();
  await expect(page.getByTestId("tape")).toContainText("Time");
  await expect(page.getByTestId("tape")).toContainText("green buy · red sell · amber AUC");
});
