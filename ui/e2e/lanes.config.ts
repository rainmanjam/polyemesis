import { defineConfig, devices } from "@playwright/test";

/* The dashboard-at-N-programmes shots.
 *
 * Its own config rather than a --grep against capture.config.ts, for one
 * reason: this one runs THREE TIMES against a single install that gains a
 * programme between runs, and each run must reuse the session the first one
 * established. capture.config.ts re-runs its setup project every invocation,
 * which is correct there and would sign in again here -- harmless, but it also
 * carries video:on, a 180s timeout and an outputDir that would drop three
 * near-identical tour videos beside the stills.
 *
 * Run it through scripts/capture-lanes.sh, which owns the staging. Pointed at
 * an install by hand it will refuse rather than mislead: lanes.spec.ts asserts
 * the programme count from both directions and fails if LANES_N disagrees with
 * what is on screen.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /lanes\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  // Generous, because the shot waits for a real stream to reach a real
  // destination -- but well short of capture.config.ts, which also waits for
  // loudness integration.
  timeout: 180_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  outputDir: `${process.env.SHOT_DIR ?? "../../docs/media"}/.playwright-lanes`,
  use: {
    baseURL: process.env.BASE_URL ?? "http://127.0.0.1:8099",
    ignoreHTTPSErrors: true,
    ...devices["Desktop Chrome"],
    // TALL ON PURPOSE, and this is the one place these shots deliberately
    // differ from capture.config.ts.
    //
    // The width matches, so the layout is the same one every other screenshot
    // shows. The height does not, because the subject is a LIST whose length
    // is the variable: at 900 the three-programme shot ran out of frame after
    // the second lane, which is a picture of two lanes filed under three -- the
    // exact mistake lanes.spec.ts asserts against, arrived at through framing
    // instead of seeding.
    //
    // fullPage is not the fix and was tried first. The app scrolls in an inner
    // container, so Playwright's fullPage returns the viewport and reports
    // success; the only way to see more of the list is to make the window
    // taller. 1800 fits three lanes with room to spare, and leaves the
    // one-programme shot with empty space below its grid -- which is honest:
    // that install genuinely has less to show.
    viewport: { width: 1440, height: 1800 },
    deviceScaleFactor: 2,
    // No video. Three runs of one test would write three clips of a page
    // sitting still, next to the stills they are not part of.
    video: "off",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    {
      name: "lanes",
      dependencies: ["setup"],
      testMatch: /lanes\.spec\.ts/,
      use: { storageState: "e2e/.auth/state.json" },
    },
  ],
});
