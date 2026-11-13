import { defineConfig, devices } from "@playwright/test";

import { titlesFor } from "./shotManifest";

// "sports" or "full"; docs/media/shots.json says which tests belong to which.
const scene = process.env.SHOT_SCENE === "sports" ? "sports" : "full";

/* Media capture, not a test suite.
 *
 * Separate from playwright.config.ts on purpose. That config is a gate: it
 * fails builds, runs in CI, and its screenshots exist only to explain a
 * failure. This one produces artefacts a human puts in a README, so it wants
 * the opposite settings — a retina scale factor, a
 * generous timeout because it waits for real audio to move a real meter, and
 * no place in CI at all.
 *
 * Run it through scripts/demo-seed.sh, which brings up a server seeded with
 * three programmes, a rendition ladder, destinations across every supported
 * platform and a recording library, and pushes live multitrack audio through
 * it. Pointed at an empty install it produces empty screenshots, which is worse
 * than none. scripts/capture-media.sh remains for the narrower single-source
 * arrangement it was written for.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /capture\.spec\.ts/,
  grep: titlesFor(scene),
  fullyParallel: false,
  workers: 1,
  retries: 0,
  // Meters need real signal to arrive before they read anything, and the
  // capture waits for that rather than photographing a zeroed bar.
  timeout: 180_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  // Beside the stills, wherever those are going, so one run's scratch files do
  // not sit in another run's output.
  outputDir: `${process.env.SHOT_DIR ?? "../../docs/media"}/.playwright`,
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
    // The screenshots are dark: a capture on a light-mode machine must not
    // photograph the other theme. System Chrome, not the bundled Chromium, so
    // the fonts and rendering are those of the browser people actually use.
    colorScheme: "dark",
    channel: "chrome",
    // No video: it existed only for the tour test, which is gone.
    video: "off",
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
