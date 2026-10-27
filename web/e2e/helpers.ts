// SPDX-License-Identifier: MIT
import type { Page } from "@playwright/test";
export async function openAdvanced(page: Page): Promise<void> {
  const disclosure = page.getByTestId("ticket-advanced");
  if (!(await disclosure.evaluate((el) => (el.parentElement as HTMLDetailsElement).open))) await disclosure.click();
}
