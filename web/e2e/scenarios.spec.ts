// SPDX-License-Identifier: MIT
import { expect, test } from "@playwright/test";
const scenarios = [
  "opening-auction",
  "stop-cascade",
  "stp-modes",
  "iceberg-priority",
  "cancel-on-disconnect",
  "fix-recovery",
];
for (const id of scenarios) {
  test(`${id}: real assertions repeat with the same seed on fresh symbols`, async ({
    page,
  }) => {
    test.setTimeout(100_000);
    await page.request.post("/api/sim/stop");
    await page.goto("/");
    await page.getByTestId("login-alice").click();
    await page.getByRole("button", { name: "Scenarios", exact: true }).click();
    await page.getByLabel("Scenario seed").fill("71");
    let previous: string[] = [];
    let symbol = "";
    for (let repetition = 0; repetition < 2; repetition++) {
      await page.getByTestId(`scenario-run-${id}`).click();
      await expect(page.getByTestId("scenario-state")).toHaveAttribute(
        "data-state",
        "running",
      );
      await expect(page.locator('[data-highlighted="true"]')).toHaveCount(1);
      await expect(page.getByTestId("scenario-state")).toHaveAttribute(
        "data-state",
        "passed",
        { timeout: 45_000 },
      );
      const currentSymbol = await page
        .getByTestId("scenario-symbol")
        .innerText();
      expect(currentSymbol).toMatch(/^SCN-/);
      expect(currentSymbol).not.toBe(symbol);
      symbol = currentSymbol;
      const outcomes = await page
        .getByTestId("scenario-step")
        .evaluateAll((rows) =>
          rows.map(
            (r) =>
              `${r.getAttribute("data-status")}:${r.getAttribute("data-outcome")}`,
          ),
        );
      expect(outcomes.length).toBeGreaterThanOrEqual(3);
      expect(outcomes.every((v) => v.startsWith("passed:"))).toBe(true);
      if (repetition) expect(outcomes).toEqual(previous);
      previous = outcomes;
      await expect(page.getByRole("status")).toContainText("cleaned up");
      if (id === "cancel-on-disconnect") {
        for (const label of ["cod-0", "cod-1"])
          await expect(
            page
              .getByTestId("scenario-blotter")
              .locator("tbody tr")
              .filter({ hasText: label }),
          ).toContainText("canceled");
      }
      if (id === "fix-recovery") {
        await expect(page.getByTestId("scenario-evidence")).toContainText(
          "Recovered resend: 34=2 | 43=Y",
        );
        await expect(page.getByTestId("scenario-evidence")).toContainText(
          "17=X",
        );
      }
      if (id === "stp-modes")
        await expect(
          page.getByLabel("STP mode comparison").locator("article"),
        ).toHaveCount(5);

      await expect(
        page.getByTestId("scenario-ladder").getByTestId("bid-row"),
      ).toHaveCount(0);
      await expect(
        page.getByTestId("scenario-ladder").getByTestId("ask-row"),
      ).toHaveCount(0);
    }
  });
}
