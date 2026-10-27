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
