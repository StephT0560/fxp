// SPDX-License-Identifier: MIT
// Keeps ONE logged-in console page open across core disruptions and requires
// it to (a) leave "live" while core is down, with Submit disabled, and (b)
// return to live and acknowledge the FIRST order sent afterwards, without a
// reload, within the deadline. Disruption commands come from the environment:
// FXP_STOP, FXP_START, FXP_RESTART, FXP_KILL (each run with bash -c).
// usage: node web/scripts/resilience-probe.mjs [deadline-seconds]
import { execFileSync } from "node:child_process";
import { chromium } from "@playwright/test";

const url = process.env.FXP_CONSOLE_URL ?? "http://127.0.0.1:3000";
const deadline = Number(process.argv[2] ?? 30) * 1000;
const browser = await chromium.launch({
  headless: true,
  ...(process.env.FXP_CONSOLE_CHROMIUM ? { executablePath: process.env.FXP_CONSOLE_CHROMIUM } : {}),
});
const page = await (await browser.newContext()).newPage();
const conn = page.getByTestId("conn-state");
const run = (name) => execFileSync("bash", ["-c", process.env[name]], { stdio: ["ignore", "ignore", "inherit"] });

async function firstOrderAcked(label) {
  const result = page.getByTestId("ticket-result");
  const before = (await result.count()) ? ((await result.textContent()) ?? "") : "";
  await page.getByTestId("ticket-side-buy").click();
  await page.getByTestId("ticket-type").selectOption({ label: "Limit" });
  await page.getByTestId("ticket-tif").selectOption({ label: "GTC" });
  await page.getByTestId("ticket-qty").fill("1");
  await page.getByTestId("ticket-price").fill("1.00");
  await page.getByTestId("ticket-submit").click({ timeout: 5000 });
  await page.waitForFunction(
    (prev) => {
      const t = document.querySelector('[data-testid="ticket-result"]')?.textContent ?? "";
      return t !== prev && /: (new|rejected)/.test(t);
    },
    before,
    { timeout: 10_000 },
  ).catch(() => {
    throw new Error(`${label}: first order after recovery was not acknowledged within 10s`);
  });
  const text = (await result.textContent()) ?? "";
  if (!/: new/.test(text)) throw new Error(`${label}: first order rejected: ${text}`);
  await page.getByTestId("cancel-all-scope").selectOption("all");
  await page.getByTestId("cancel-all").click();
}

async function liveAgain(label, started) {
  await conn.filter({ hasText: /^live/ }).waitFor({ timeout: deadline }).catch(() => {
    throw new Error(`${label}: same page not live again within ${deadline / 1000}s`);
  });
  await firstOrderAcked(label);
  console.log(`PASS ${label}: same page live and first order acked after ${((Date.now() - started) / 1000).toFixed(1)}s`);
}

try {
  await page.goto(url);
  await page.getByTestId("login-alice").click();
  await conn.filter({ hasText: /^live/ }).waitFor({ timeout: deadline });
  await page.getByTestId("symbol-DEMO").click();
  await firstOrderAcked("baseline");

  run("FXP_STOP");
  // While core is down the page must not claim to be live, and must not offer Submit.
  await page.waitForFunction(() => !/^live/.test(document.querySelector('[data-testid="conn-state"]')?.textContent ?? ""), null, { timeout: 15_000 })
    .catch(() => { throw new Error("core stopped: console still shows live after 15s"); });
  await page.waitForTimeout(3000);
  if (/^live/.test((await conn.textContent()) ?? "")) throw new Error("core stopped: console went back to live while core is down");
  if (!(await page.getByTestId("ticket-submit").isDisabled())) throw new Error("core stopped: Submit enabled while core is down");
  console.log(`PASS core stopped: console shows "${(await conn.textContent())?.trim()}", Submit disabled`);
  let started = Date.now();
  run("FXP_START");
  await liveAgain("compose stop core && start core", started);

  started = Date.now();
  run("FXP_RESTART");
  await liveAgain("compose restart core", started);

  started = Date.now();
  run("FXP_KILL");
  await liveAgain("killed core process (restart policy)", started);
} catch (e) {
  console.error(`FAIL ${String(e?.message ?? e).split("\n")[0]}`);
  const text = async (id) => ((await page.getByTestId(id).count()) ? (await page.getByTestId(id).first().textContent())?.trim() : "(absent)");
  for (const id of ["conn-state", "ticket-result", "ticket-errors", "phase"]) console.error(`  ${id}: ${await text(id)}`);
  await browser.close();
  process.exit(1);
}
await browser.close();
