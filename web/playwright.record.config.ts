// SPDX-License-Identifier: MIT
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "record",
  outputDir: "recordings/results",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  workers: 1,
  fullyParallel: false,
  retries: 0,
  reporter: "list",
  use: {
    baseURL: process.env.FXP_CONSOLE_URL ?? "http://127.0.0.1:3000",
    viewport: { width: 1920, height: 1080 },
    // Video is enabled explicitly on Alice's context, never Bob's.
    video: "off",
    trace: "off",
    launchOptions: {
      slowMo: 60,
      ...(process.env.FXP_CONSOLE_CHROMIUM
        ? { executablePath: process.env.FXP_CONSOLE_CHROMIUM }
        : {}),
    },
  },
});
