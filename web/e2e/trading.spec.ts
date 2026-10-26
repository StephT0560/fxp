// SPDX-License-Identifier: MIT
import { expect, test, type Browser, type Page, type WebSocketRoute } from "@playwright/test";

// DEMO is configured but never traded by the simulator, so fills are deterministic.
const SYMBOL = "DEMO";
const LIMIT = "2";
const AT_OPEN = "5";

test.describe.configure({ mode: "serial" });

async function login(browser: Browser, user: string, onSocket?: (ws: WebSocketRoute) => void): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  if (onSocket) {
    await page.routeWebSocket(/\/ws$/, (ws) => onSocket(ws));
  }
  await page.goto("/");
  await page.getByTestId(`login-${user}`).click();
  await expect(page.getByTestId("conn-state")).toContainText("live", { timeout: 15_000 });
  await page.getByTestId(`symbol-${SYMBOL}`).click();
  return page;
}

async function clean(page: Page): Promise<void> {
  await page.getByTestId("cancel-all-scope").selectOption("all");
  await page.getByTestId("cancel-all").click();
  await expect(page.getByTestId("blotter-notice")).toContainText("cancel-all (all symbols)");
}

async function continuous(page: Page): Promise<void> {
  await expect(page.getByTestId("phase")).toContainText("Continuous", { timeout: 60_000 });
}

/** Places a limit order and returns its client order id. */
async function place(page: Page, side: "buy" | "sell", qty: number, price: string, tif = "2"): Promise<string> {
  await page.getByTestId(`ticket-side-${side}`).click();
  await page.getByTestId("ticket-type").selectOption(LIMIT);
  await page.getByTestId("ticket-tif").selectOption(tif);
  await page.getByTestId("ticket-qty").fill(String(qty));
  await page.getByTestId("ticket-price").fill(price);
  await expect(page.getByTestId("ticket-submit")).toBeEnabled();
  const result = page.getByTestId("ticket-result");
  const before = (await result.count()) > 0 ? await result.textContent() : null;
  await page.getByTestId("ticket-submit").click();
  await expect(result).not.toHaveText(before ?? "", { timeout: 5_000 });
  await expect(result).not.toContainText("pending", { timeout: 5_000 });
  return (await result.textContent())!.split(":")[0]!.trim();
}

const row = (page: Page, id: string) => page.locator(`[data-testid=order-row][data-order-id="${id}"]`);

test("two users: rest, cross, partial fill, replace, rejects and cancel", async ({ browser }) => {
  const a = await login(browser, "alice");
  const b = await login(browser, "bob");
  await continuous(a);
  await clean(a);
  await clean(b);

  // Client-side validation from /api/symbols.
  await a.getByTestId("ticket-price").fill("50.001");
  await expect(a.getByTestId("ticket-errors")).toContainText("2 decimal places");

  const resting = await place(a, "buy", 100, "50.00");
  await expect(row(a, resting)).toHaveAttribute("data-state", "new");
  await expect(a.getByTestId("bid-row").first()).toContainText("50.00");

  const crossing = await place(b, "sell", 40, "50.00");
  await b.getByTestId("blotter-tab-completed").click();
  await expect(row(b, crossing)).toHaveAttribute("data-state", "filled");
  await expect(row(a, resting)).toHaveAttribute("data-state", "partial");
  await expect(row(a, resting).getByTestId("order-cum")).toHaveText("40");
  await expect(row(a, resting).getByTestId("order-leaves")).toHaveText("60");
  await expect(a.getByTestId("tape-row").first()).toContainText("50.00");
  await expect(a.getByTestId("tape-row").first()).toContainText("40");
  for (const p of [a, b]) {
    await p.getByTestId("blotter-tab-fills").click();
    await expect(p.getByTestId("own-fill-row").first()).toContainText("40");
    await expect(p.getByTestId("own-fill-row").first()).toContainText("50.00");
  }

  // Replace: new price, same quantity; the original moves to completed.
  await a.getByTestId("blotter-tab-working").click();
  await row(a, resting).getByTestId("order-replace").click();
  await a.getByTestId("replace-price").fill("50.10");
  await a.getByTestId("replace-submit").click();
  const replaced = a.locator("[data-testid=order-row]").filter({ hasText: "50.10" });
  await expect(replaced).toHaveAttribute("data-state", "partial");
  await expect(replaced.getByTestId("order-leaves")).toHaveText("60");
  const replacement = (await replaced.getAttribute("data-order-id"))!;
  await a.getByTestId("blotter-tab-completed").click();
  await expect(row(a, resting)).toHaveAttribute("data-state", "replaced");

  // The core rejects a quantity below the filled amount; the order is unchanged.
  await a.getByTestId("blotter-tab-working").click();
  await row(a, replacement).getByTestId("order-replace").click();
  await a.getByTestId("replace-qty").fill("30");
  await a.getByTestId("replace-submit").click();
  await expect(row(a, replacement).getByTestId("order-reason")).toContainText("replace rejected");
  await expect(row(a, replacement)).toHaveAttribute("data-state", "partial");

  // Server reject with its reason: at-open outside an opening call.
  await place(a, "buy", 10, "49.00", AT_OPEN);
  await expect(a.getByTestId("ticket-result")).toContainText("rejected");
  await expect(a.getByTestId("ticket-result")).toContainText(/no auction scheduled|opening auction not in progress/);

  await row(a, replacement).getByTestId("order-cancel").click();
  await a.getByTestId("blotter-tab-completed").click();
  await expect(row(a, replacement)).toHaveAttribute("data-state", "canceled");
  await a.getByTestId("blotter-tab-log").click();
  await expect(a.getByTestId("exec-log")).toContainText("CANCELED");
});

test("cancel-all by symbol, then all symbols", async ({ browser }) => {
  const a = await login(browser, "alice");
  await continuous(a);
  await clean(a);
  const demo = await place(a, "buy", 10, "40.00");
  await a.getByTestId("symbol-AAPL").click();
  const aapl = await place(a, "buy", 10, "1.00");
  await a.getByTestId(`symbol-${SYMBOL}`).click();
  await a.getByTestId("cancel-all-scope").selectOption("symbol");
  await a.getByTestId("cancel-all").click();
  await expect(a.getByTestId("blotter-notice")).toContainText(`cancel-all (${SYMBOL}): 1 canceled`);
  await expect(row(a, aapl)).toHaveAttribute("data-state", /new|pending/);
  await a.getByTestId("blotter-tab-completed").click();
  await expect(row(a, demo)).toHaveAttribute("data-state", "canceled");
  await a.getByTestId("cancel-all-scope").selectOption("all");
  await a.getByTestId("cancel-all").click();
  await expect(a.getByTestId("blotter-notice")).toContainText("cancel-all (all symbols): 1 canceled");
  await expect(row(a, aapl)).toHaveAttribute("data-state", "canceled");
});

test("socket drop and page refresh rebuild the blotter from order status", async ({ browser }) => {
  // Proxy alice's socket so the test can drop it and hold reconnects off.
  let socket: WebSocketRoute | undefined;
  let blocked = false;
  const a = await login(browser, "alice", (ws) => {
    if (blocked) {
      void ws.close({ code: 1013, reason: "test: gateway unavailable" });
      return;
    }
    ws.connectToServer();
    socket = ws;
  });
  const b = await login(browser, "bob");
  await continuous(a);
  await clean(a);
  await clean(b);
  const id = await place(a, "buy", 100, "45.00");
  await expect(row(a, id)).toHaveAttribute("data-state", "new");

  // Filled while alice is offline: only the order-status rebuild can show it.
  blocked = true;
  await socket!.close({ code: 1012, reason: "test: socket dropped" });
  await expect(a.getByTestId("conn-state")).toContainText("reconnecting", { timeout: 15_000 });
  await expect(a.getByTestId("blotter-stale")).toBeVisible();
  await place(b, "sell", 100, "45.00");
  blocked = false;
  await expect(a.getByTestId("conn-state")).toContainText("live", { timeout: 20_000 });
  await expect(a.getByTestId("blotter-stale")).toBeHidden();
  await a.getByTestId("blotter-tab-completed").click();
  await expect(row(a, id)).toHaveAttribute("data-state", "filled");
  await expect(row(a, id).getByTestId("order-cum")).toHaveText("100");

  const working = await place(a, "buy", 10, "44.00");
  await a.reload();
  await a.getByTestId("login-alice").click();
  await expect(a.getByTestId("conn-state")).toContainText("live", { timeout: 15_000 });
  await a.getByTestId(`symbol-${SYMBOL}`).click();
  await expect(row(a, working)).toHaveAttribute("data-state", "new");
  await expect(row(a, working).getByTestId("order-leaves")).toHaveText("10");
  await a.getByTestId("blotter-tab-log").click();
  await expect(a.getByTestId("exec-log")).toContainText("rebuilt from order status");
  // Reports for orders entered before the refresh reach the new socket.
  await clean(a);
  await a.getByTestId("blotter-tab-completed").click();
  await expect(row(a, working)).toHaveAttribute("data-state", "canceled");
});

test("rapid clicks submit one order", async ({ browser }) => {
  const a = await login(browser, "alice");
  await continuous(a);
  await clean(a);
  await a.getByTestId("ticket-side-buy").click();
  await a.getByTestId("ticket-type").selectOption(LIMIT);
  await a.getByTestId("ticket-tif").selectOption("2");
  await a.getByTestId("ticket-qty").fill("7");
  await a.getByTestId("ticket-price").fill("30.00");
  await a.getByTestId("ticket-submit").dblclick();
  await a.getByTestId("ticket-submit").click({ force: true });
  await a.getByTestId("blotter-tab-working").click();
  await expect(a.locator("[data-testid=order-row]")).toHaveCount(1);
  await a.waitForTimeout(500);
  await expect(a.locator("[data-testid=order-row]")).toHaveCount(1);
  await clean(a);
});

test("opening auction uncrosses on the tape with auction prints and own auction fills", async ({ browser }) => {
  test.setTimeout(150_000);
  const a = await login(browser, "alice");
  const b = await login(browser, "bob");
  await clean(a);
  await clean(b);
  await a.request.post("/api/sim/stop", { headers: { Origin: new URL(a.url()).origin } });
  await a.getByTestId("sim-auction").check();
  await a.getByTestId("sim-auction-secs").fill("20");
  await a.getByTestId("sim-duration").fill("90");
  await a.getByTestId("sim-start").click();
  await expect(a.getByTestId("sim-msg")).toContainText("opening auction for 20s");
  await expect(a.getByTestId("phase")).toContainText("Opening call", { timeout: 10_000 });
  await expect(b.getByTestId("phase")).toContainText("Opening call", { timeout: 10_000 });

  // Two at-open orders on DEMO cross only at the uncross.
  const buy = await place(a, "buy", 25, "60.00", AT_OPEN);
  const sell = await place(b, "sell", 25, "60.00", AT_OPEN);
  await expect(row(a, buy)).toHaveAttribute("data-state", "new");
  await expect(row(b, sell)).toHaveAttribute("data-state", "new");
  await expect(a.getByTestId("auction-banner")).toBeVisible();

  await expect(a.getByTestId("phase")).toContainText("Continuous", { timeout: 40_000 });
  await expect(a.getByTestId("tape-row").filter({ hasText: "AUC" }).first()).toContainText("60.00");
  await a.getByTestId("blotter-tab-fills").click();
  await expect(a.getByTestId("own-fill-row").first()).toContainText("auction");

  // The simulator's symbols uncross too.
  await a.getByTestId("symbol-AAPL").click();
  await expect(a.getByTestId("tape-row").filter({ hasText: "AUC" }).first()).toBeVisible({ timeout: 20_000 });
});
