// SPDX-License-Identifier: MIT
import { expect, test, type Page } from "@playwright/test";

const ladderText = (page: Page) => page.getByTestId("ladder").innerText();
const topPrint = (page: Page) => page.getByTestId("tape-row").first().innerText();

test("login, start simulator, book and tape update within 5s", async ({ page }) => {
  await page.goto("/");
  await page.getByTestId("login-alice").click();
  await expect(page.getByTestId("conn-state")).toContainText("live", { timeout: 10_000 });
  await page.getByTestId("symbol-AAPL").click();
  await page.getByTestId("sim-start").click();
  await expect(page.getByTestId("sim-msg")).toContainText(/running|already/);

  // A cold simulator spends its pre-market before quoting and its opening
  // call before the first print; the 5 s budget applies to the live stream.
  await expect(page.getByTestId("bid-row").first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("ask-row").first()).toBeVisible({ timeout: 5_000 });
  await expect(page.getByTestId("phase")).not.toContainText("Unknown", { timeout: 5_000 });
  await expect(page.getByTestId("tape-row").first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("last-price")).not.toHaveText("—");
  await expect(page.getByTestId("gap-counter")).toBeVisible();

  const book = await ladderText(page);
  const print = await topPrint(page);
  await expect.poll(() => ladderText(page), { timeout: 5_000 }).not.toBe(book);
  await expect.poll(() => topPrint(page), { timeout: 5_000 }).not.toBe(print);
});
