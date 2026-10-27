// SPDX-License-Identifier: MIT
import { expect, test } from "@playwright/test";

test("recorded latency shows both endpoints, spread, provenance and profile", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByTestId("login-alice").click();
  await page.getByRole("button", { name: "Latency", exact: true }).click();
  await expect(
    page.getByTestId("latency-chart").locator("svg").first(),
  ).toBeVisible();
  await expect(page.getByTestId("latency-spread")).toContainText("108.528");
  await expect(page.getByTestId("latency-spread")).toContainText(
    "Intended-time ACK",
  );
  await expect(page.getByTestId("latency-spread")).toContainText(
    "Post-send ACK",
  );
  await expect(page.getByTestId("environment-card")).toContainText("loopback");
  await expect(page.getByTestId("environment-card")).toContainText("f6148617");
  await page.getByLabel("Benchmark scenario").selectOption("closed-c1");
  await expect(page.getByTestId("environment-card")).toContainText("closed");
  await expect(page.getByTestId("latency-spread")).toContainText("0.141");
  await expect(
    page.getByRole("link", { name: "Read profile-r4.md" }),
  ).toHaveAttribute("href", "/api/bench/profile-r4");
  const profile = await page.request.get("/api/bench/profile-r4");
  expect(await profile.text()).toContain("Round-4 profiling");
});

for (const body of [
  "null",
  '{"schema_version":1,"scenarios":[]}',
  '{"broken":',
]) {
  test(`latency empty state: ${body}`, async ({ page }) => {
    await page.route("**/api/bench", (r) =>
      r.fulfill({ contentType: "application/json", body }),
    );
    await page.goto("/");
    await page.getByTestId("login-bob").click();
    await page.getByRole("button", { name: "Latency", exact: true }).click();
    await expect(page.getByRole("status")).toContainText(
      "unavailable or malformed",
    );
    await expect(page.getByTestId("latency-chart")).toHaveCount(0);
  });
}

test("WS frames, redacted Logon, filters, hex and authoritative FIX comparison", async ({
  page,
}) => {
  // The preceding smoke test leaves the simulator running. Stop it before
  // login so the bounded 200-frame history retains the handshake we inspect.
  await page.request.post("/api/sim/stop");
  let jwt = "";
  page.on("response", async (res) => {
    if (res.url().endsWith("/api/token") && res.ok())
      jwt = (await res.json()).token;
  });
  await page.goto("/");
  await page.getByTestId("login-alice").click();
  await expect(page.getByTestId("conn-state")).toContainText("live", {
    timeout: 15_000,
  });
  await page
    .getByRole("button", { name: "Protocol inspector", exact: true })
    .click();
  await page.getByLabel("Frame type", { exact: true }).selectOption("logon");
  await page.getByRole("button", { name: "logon", exact: true }).click();
  await expect(page.getByTestId("frame-detail")).toContainText("[REDACTED]");
  await expect(page.getByTestId("frame-detail")).toContainText("Withheld");
  expect(jwt).not.toBe("");
  expect(await page.locator("body").innerText()).not.toContain(jwt);
  await page.getByRole("button", { name: "Market", exact: true }).click();
  await page.getByTestId("symbol-DEMO").click();
  await page.getByTestId("ticket-price").fill("49.00");
  await page.getByTestId("ticket-qty").fill("1");
  await page.getByTestId("ticket-stp").selectOption("0");
  await page.getByTestId("ticket-submit").click();
  await page
    .getByRole("button", { name: "Protocol inspector", exact: true })
    .click();
  await page.getByLabel("Frame type", { exact: true }).selectOption("newOrder");
  await page.getByLabel("Frame symbol", { exact: true }).fill("DEMO");
  await page
    .getByLabel("Frame direction", { exact: true })
    .selectOption("sent");
  await page
    .getByRole("button", { name: "newOrder", exact: true })
    .last()
    .click();
  await expect(page.getByTestId("frame-detail")).toContainText("DEMO");
  await expect(page.getByTestId("frame-detail")).not.toContainText("Withheld");
  await page.getByRole("button", { name: "FXP vs FIX for this order" }).click();
  await expect(page.getByRole("status")).toContainText("8=FIX.4.4|");
  await expect(page.getByRole("status")).toContainText("55=DEMO|");
  await expect(page.getByRole("status")).toContainText(
    /FXP \d+ bytes · FIX 4.4 \d+ bytes/,
  );
  await page
    .getByLabel("Frame direction", { exact: true })
    .selectOption("received");
  await expect(
    page.getByRole("button", { name: "newOrder", exact: true }),
  ).toHaveCount(0);
});
