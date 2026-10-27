// SPDX-License-Identifier: MIT
import { expect, test } from "@playwright/test";
for (const [width, height] of [[960,1080],[1280,720],[1366,768],[1920,1080]]) {
  test(`market and panels fit ${width}x${height}`, async ({ page }) => {
    await page.setViewportSize({ width: width!, height: height! });
    await page.goto("/"); await page.getByTestId("login-alice").click();
    await expect(page.getByTestId("chart")).toBeVisible();
    if (width! < 1200) await page.getByTestId("symbol-select").selectOption("DEMO");
    else await page.getByTestId("symbol-DEMO").click();
    await page.getByTestId("ticket-qty").fill("1");
    await page.getByTestId("ticket-price").fill("27.00");
    await page.getByTestId("ticket-submit").click();
    await expect(page.getByTestId("ladder").getByTestId("bid-row").first()).toBeVisible();
    const metrics = await page.evaluate(() => {
      const ladder = document.querySelector('[data-testid="ladder"]')!;
      const bounds = ladder.getBoundingClientRect();
      const rows = [...ladder.querySelectorAll('.ladder-row')];
      return { width: innerWidth, height: innerHeight, scrollWidth: document.documentElement.scrollWidth,
        chart: document.querySelector('[data-testid="chart"]')!.getBoundingClientRect().width,
        visibleRows: rows.filter((row) => {const r = row.getBoundingClientRect();return r.top >= bounds.top && r.bottom <= bounds.bottom;}).length,
        ladderScroll: ladder.scrollHeight > ladder.clientHeight };
    });
    console.log(JSON.stringify(metrics));
    expect(metrics.scrollWidth).toBeLessThanOrEqual(width!); expect(metrics.chart).toBeGreaterThanOrEqual(320);
    if (width! >= 1366) { expect(metrics.visibleRows).toBe(20); expect(metrics.ladderScroll).toBe(false); }
    if (width! < 1200) await expect(page.getByTestId("symbol-select")).toBeVisible();
    else await expect(page.getByTestId("symbol-AAPL")).toBeVisible();
    for (const name of ["Scenarios", "Latency", "Protocol inspector"]) {
      await page.getByRole("button", { name, exact: true }).click();
      if (name === "Scenarios") {
        await expect(page.getByTestId("scenario-run-stp-modes")).toBeVisible();
        if (width === 960) {
          await page.getByTestId("scenario-run-stp-modes").click();
          await expect(page.getByTestId("scenario-state")).toHaveAttribute("data-state", "passed", { timeout: 45000 });
          await expect(page.getByLabel("STP mode comparison").locator("article")).toHaveCount(5);
        }
      } else if (name === "Latency") await expect(page.getByTestId("latency-spread")).toBeVisible();
      else await expect(page.getByTestId("frame-table")).toBeVisible();
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width!);
    }
  });
}
