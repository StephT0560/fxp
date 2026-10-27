// SPDX-License-Identifier: MIT
import { expect, test, type Page } from "@playwright/test";
import { mkdir, stat } from "node:fs/promises";
import path from "node:path";

const viewport = { width: 1920, height: 1080 };
const media = path.resolve(import.meta.dirname, "../../docs/media");
const recordings = path.resolve(import.meta.dirname, "../recordings");


async function login(page: Page, user: string) {
  console.log(`Login ${user}`);
  await page.goto("/");
  await page.getByTestId(`login-${user}`).click();
  await expect(page.getByTestId("conn-state")).toContainText("live");
  await page.getByTestId("symbol-DEMO").click();
}

async function place(page: Page, side: "buy" | "sell", qty: number, price: string, tif = "2") {
  await page.getByTestId(`ticket-side-${side}`).click();
  await page.getByTestId("ticket-type").selectOption("2");
  await page.getByTestId("ticket-tif").selectOption(tif);
  await page.getByTestId("ticket-qty").fill(String(qty));
  await page.getByTestId("ticket-price").fill(price);
  const result = page.getByTestId("ticket-result");
  const previous = await result.count() ? await result.textContent() : "";
  await page.getByTestId("ticket-submit").click();
  await expect(result).not.toHaveText(previous ?? "");
  await expect(result).not.toContainText("pending");
  await expect(result).not.toContainText("rejected");
  await expect(page.getByTestId("ticket-submit")).toBeEnabled();
  return (await result.innerText()).split(":")[0]!.trim();
}

async function still(page: Page, name: string) {
  const file = path.join(media, name);
  // Chromium emits compressed PNG; CSS scale keeps a 1920x1080 image even on HiDPI hosts.
  await page.mouse.move(1800, 20);
  await page.screenshot({ path: file, animations: "disabled", scale: "css" });
  expect((await stat(file)).size, `${name} must stay <=250 KB`).toBeLessThanOrEqual(250_000);
}

test("three-beat presentation: Alice only", async ({ browser, baseURL }) => {
  await mkdir(media, { recursive: true });
  await mkdir(recordings, { recursive: true });
  const aliceContext = await browser.newContext({
    baseURL, viewport,
    recordVideo: { dir: path.join(recordings, "raw"), size: viewport },
  });
  const bobContext = await browser.newContext({ baseURL, viewport });
  const alice = await aliceContext.newPage();
  const bob = await bobContext.newPage();
  const video = alice.video()!;
  const started = Date.now();
  const holdUntil = async (second: number) => {
    const remaining = started + second * 1000 - Date.now();
    if (remaining > 0) await alice.waitForTimeout(remaining);
  };
  try {
    // This owns the demo take. Run only against an idle local stack.
    expect((await alice.request.post("/api/sim/stop", { timeout: 10_000 })).ok()).toBe(true);
    await login(bob, "bob");
    await login(alice, "alice");
    for (const p of [alice, bob]) {
      await p.getByTestId("cancel-all-scope").selectOption("all");
      await p.getByTestId("cancel-all").click();
      await expect(p.getByTestId("blotter-notice")).toContainText("cancel-all (all symbols)");
    }
    await alice.getByTestId("sim-seed").fill("42");
    await alice.getByTestId("sim-duration").fill("120");
    await alice.getByTestId("sim-auction").check();
    await alice.getByTestId("sim-auction-secs").fill("10");
    console.log("Beat 1: opening auction");
    await alice.getByTestId("sim-start").click();
    await expect(alice.getByTestId("auction-countdown")).toContainText(/uncross in \d+ s/);
    await expect(bob.getByTestId("phase")).toContainText("Opening call");
    await place(bob, "sell", 25, "60.00", "5");
    await place(alice, "buy", 25, "60.00", "5");
    await expect(alice.getByTestId("auction-banner")).toBeVisible();
    await expect(alice.getByTestId("auction-countdown")).toBeVisible();
    await still(alice, "console-auction.png");
    await expect(alice.getByTestId("phase")).toContainText("Continuous", { timeout: 20_000 });
    await expect(alice.getByTestId("tape-row").filter({ hasText: "AUC" }).first()).toContainText("60.00");
    await holdUntil(20);
    await alice.getByTestId("sim-stop").click();
    await expect(alice.getByTestId("sim-msg")).toContainText("stopped");

    console.log("Beat 2: partial fill and replace");
    await place(bob, "sell", 40, "50.00");
    const id = await place(alice, "buy", 100, "50.00");
    const partial = alice.locator(`[data-testid="order-row"][data-order-id="${id}"]`);
    await expect(partial).toHaveAttribute("data-state", "partial");
    await expect(partial.getByTestId("order-cum")).toHaveText("40");
    await expect(partial.getByTestId("order-leaves")).toHaveText("60");
    await expect(alice.getByTestId("tape-row").first()).toContainText("50.00");
    await holdUntil(30);
    await partial.getByTestId("order-replace").click();
    await alice.getByTestId("replace-price").fill("50.10");
    await alice.getByTestId("replace-submit").click();
    const replaced = alice.getByTestId("order-row").filter({ hasText: "50.10" });
    await expect(replaced).toHaveAttribute("data-state", "partial");
    await expect(replaced.getByTestId("order-leaves")).toHaveText("60");
    const level = alice.getByTestId("bid-row").filter({ hasText: "50.10" });
    await expect(level.getByTestId("own-order-marker")).toContainText("60");
    await still(alice, "console-partial-fill.png");
    await holdUntil(48);

    console.log("Beat 3: bytes and recorded latency");
    await alice.getByRole("button", { name: "Protocol inspector", exact: true }).click();
    await alice.getByLabel("Frame type", { exact: true }).selectOption("newOrder");
    await alice.getByLabel("Frame symbol", { exact: true }).fill("DEMO");
    await alice.getByLabel("Frame direction", { exact: true }).selectOption("sent");
    await alice.getByRole("button", { name: "newOrder", exact: true }).last().click();
    await expect(alice.getByTestId("frame-detail")).toContainText(id);
    await alice.getByRole("button", { name: "FXP vs FIX for this order" }).click();
    await expect(alice.getByRole("status")).toContainText("8=FIX.4.4|");
    await expect(alice.getByRole("status")).toContainText("55=DEMO|");
    await expect(alice.getByRole("status")).toContainText(/FXP \d+ B/);
    await still(alice, "console-protocol.png");
    await holdUntil(62);
    await alice.getByRole("button", { name: "Latency", exact: true }).click();
    await expect(alice.getByTestId("latency-chart").locator("svg").first()).toBeVisible();
    await expect(alice.getByTestId("latency-spread")).toContainText("Intended-time ACK");
    await expect(alice.getByTestId("latency-spread")).toContainText("Post-send ACK");
    await expect(alice.getByTestId("latency-spread")).toContainText("Median [min–max]");
    await expect(alice.getByTestId("environment-card")).toContainText("loopback");
    await expect(alice.getByText("Capacity is a tested knee", { exact: false })).toBeVisible();
    await holdUntil(75);
  } finally {
    await alice.request.post("/api/sim/stop", { timeout: 5_000 }).catch(() => {});
    // Closing flushes the only recorded context before exporting a stable filename.
    await bobContext.close();
    await aliceContext.close();
    await video.saveAs(path.join(recordings, "fxp-console.webm"));
  }
});
