// SPDX-License-Identifier: MIT
// End-to-end console probe: log in, reach "live", and round-trip one DEMO order
// through BFF -> gateway -> core. Exits 0 on success, 1 otherwise.
// usage: node web/scripts/live-probe.mjs [deadline-seconds]
import { chromium } from "@playwright/test";

const url = process.env.FXP_CONSOLE_URL ?? "http://127.0.0.1:3000";
const deadline = Date.now() + Number(process.argv[2] ?? 30) * 1000;
const browser = await chromium.launch({
  headless: true,
  ...(process.env.FXP_CONSOLE_CHROMIUM ? { executablePath: process.env.FXP_CONSOLE_CHROMIUM } : {}),
});

async function attempt() {
  const context = await browser.newContext();
  const page = await context.newPage();
  const left = () => Math.max(1000, deadline - Date.now());
  try {
    await page.goto(url, { timeout: left() });
    await page.getByTestId("login-alice").click({ timeout: left() });
    await page.getByTestId("conn-state").filter({ hasText: /^live/ }).waitFor({ timeout: left() });
    await page.getByTestId("symbol-DEMO").click({ timeout: left() });
    await page.getByTestId("ticket-side-buy").click();
    await page.getByTestId("ticket-type").selectOption({ label: "Limit" });
    await page.getByTestId("ticket-tif").selectOption({ label: "GTC" });
    await page.getByTestId("ticket-qty").fill("1");
    await page.getByTestId("ticket-price").fill("1.00");
    await page.getByTestId("ticket-submit").click();
    const result = page.getByTestId("ticket-result");
    await result.filter({ hasText: /: (new|rejected)/ }).waitFor({ timeout: Math.min(5000, left()) });
    const text = (await result.textContent()) ?? "";
    await page.getByTestId("cancel-all-scope").selectOption("all");
    await page.getByTestId("cancel-all").click();
    await page.getByTestId("blotter-notice").filter({ hasText: "cancel-all (all symbols)" }).waitFor({ timeout: Math.min(5000, left()) });
    if (!/: new/.test(text)) throw new Error(`order not acknowledged: ${text}`);
    return text;
  } finally {
    await context.close();
  }
}

let last = "no attempt";
let ok = false;
while (Date.now() < deadline) {
  try {
    const ack = await attempt();
    console.log(`live: login, conn-state live and DEMO order ack (${ack.split(":")[1]?.trim()})`);
    ok = true;
    break;
  } catch (e) {
    last = String(e?.message ?? e).split("\n")[0];
    await new Promise((r) => setTimeout(r, 1000));
  }
}
await browser.close();
if (!ok) {
  console.error(`not live before deadline: ${last}`);
  process.exit(1);
}
