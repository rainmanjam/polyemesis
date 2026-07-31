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
      // capture.spec.ts is a media-generation tool, not a test, and it is the
      // ONE spec this config must not sweep up: it writes into docs/media/, and
      // this suite deliberately never streams. Running it here overwrote the
      // committed screenshots with pictures of an offline system -- no failure,
      // no warning, just a working tree full of degraded PNGs and a README that
      // would quietly stop showing the product working. capture.config.ts runs
      // it against a live ingest, which is the only context where it means
      // anything.
      testIgnore: /capture\.spec\.ts/,
    },
  ],
});
