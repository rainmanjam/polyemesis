import { defineConfig, devices } from "@playwright/test";

/* The browser suite runs against a REAL polyemesis, not a dev server.
 *
 * scripts/acceptance-browser.sh starts the shipped container and points
 * BASE_URL here, so what is exercised is the same artefact a user pulls --
 * embedded assets, the Go router's SPA fallback, the real API. A Vite dev
 * server would test a build that nobody runs.
 *
 * Serial, single worker, no retries. These tests mutate one shared install:
 * they create sources, delete recordings and rotate tokens, so running them in
 * parallel would have them fighting over the same rows. And a retry that turns
 * red into green hides exactly the intermittent bug worth finding. */
export default defineConfig({
  testDir: ".",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  use: {
    baseURL: process.env.BASE_URL ?? "http://127.0.0.1:8099",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    // The container serves plain HTTP; there is no certificate to verify.
    ignoreHTTPSErrors: true,
  },
  projects: [
    // Signs in once and saves the session; everything else reuses it. Without
    // this each test logs in again, and the first-run form is gone by the
    // second test -- so every test would need to handle both paths.
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    {
      name: "chromium",
      dependencies: ["setup"],
      use: { ...devices["Desktop Chrome"], storageState: "e2e/.auth/state.json" },
      testMatch: /.*\.spec\.ts/,
    },
  ],
});
