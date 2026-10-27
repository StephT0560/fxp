// SPDX-License-Identifier: MIT
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  // One demo stack is shared: specs run in order, never in parallel.
  workers: 1,
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    launchOptions: process.env.FXP_CONSOLE_CHROMIUM
      ? { executablePath: process.env.FXP_CONSOLE_CHROMIUM }
      : {},
    baseURL: process.env.FXP_CONSOLE_URL ?? "http://127.0.0.1:3000",
    trace: "on",
    video: "on",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1600, height: 1000 },
      },
    },
  ],
});
