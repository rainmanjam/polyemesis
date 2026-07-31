import { defineConfig, devices } from "@playwright/test";

/* Media capture, not a test suite.
 *
 * Separate from playwright.config.ts on purpose. That config is a gate: it
 * fails builds, runs in CI, and its screenshots exist only to explain a
 * failure. This one produces artefacts a human puts in a README, so it wants
 * the opposite settings — a retina scale factor, video on for every run, a
 * generous timeout because it waits for real audio to move a real meter, and
 * no place in CI at all.
 *
 * Run it through scripts/capture-media.sh, which brings up a seeded server
 * first. Pointed at an empty install it produces empty screenshots, which is
 * worse than none.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /capture\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  // Meters need real signal to arrive before they read anything, and the
  // capture waits for that rather than photographing a zeroed bar.
  timeout: 180_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  outputDir: "../../docs/media/.playwright",
  use: {
    baseURL: process.env.BASE_URL ?? "http://127.0.0.1:8099",
    ignoreHTTPSErrors: true,
    // NOT storageState here. The setup project is what WRITES that file, so a
    // global setting makes setup try to read a session it has not created yet
    // and the whole run dies on ENOENT before the first shot. It belongs on the
    // consuming project only -- same shape as playwright.config.ts.
    ...devices["Desktop Chrome"],
    // 1440x900 at 2x lands at 2880x1800: crisp when GitHub scales it into a
    // README column, and still a believable desktop window rather than an
    // ultrawide nobody has.
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    video: {
      mode: "on",
      // Video ignores deviceScaleFactor, so its size is set independently.
      // 1280x800 keeps the 16:10 shape of the stills.
      size: { width: 1280, height: 800 },
    },
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    {
      name: "capture",
      dependencies: ["setup"],
      testMatch: /capture\.spec\.ts/,
      use: { storageState: "e2e/.auth/state.json" },
    },
  ],
});
