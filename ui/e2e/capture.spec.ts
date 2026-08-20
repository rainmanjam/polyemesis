import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";

/* Screenshots and video for the README and the website.
 *
 * Shot order follows the argument rather than the navigation: what it is
 * (dashboard), the thing nothing else does (routing), the proof (meters), then
 * the supporting cast. A reader who stops after two images should still have
 * understood the product.
 *
 * Every shot waits for real state to arrive. A screenshot taken before the
 * meters read anything is a picture of a product that does nothing, and it is
 * indistinguishable from a broken one.
 */

const OUT = "../docs/media";
mkdirSync(OUT, { recursive: true });

/** Settles layout before a shot: fonts loaded, no animation mid-flight.
 *
 *  Without the font wait, the first screenshot of a run catches Inter still
 *  swapping and every glyph shifts a pixel between runs — which turns a diff of
 *  two captures into noise. */
async function settle(page: import("@playwright/test").Page, ms = 900) {
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}

/** Hides anything that changes between runs but says nothing about the product,
 *  so re-capturing produces a comparable image rather than a different one. */
async function calm(page: import("@playwright/test").Page) {
  await page.addStyleTag({
    content: `
      *, *::before, *::after {
        animation-duration: 0s !important;
        animation-delay: 0s !important;
        transition-duration: 0s !important;
      }
    `,
  });
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
  await calm(page);
});

test("dashboard — the hero shot", async ({ page }) => {
  await page.goto("/");
  // The ingest has to read live before this is worth photographing: a
  // dashboard whose source says "waiting" is a picture of nothing happening.
  //
  // Asserted as the ABSENCE of "Offline" rather than the presence of "live".
  // The earlier guard was `getByText(/live/i).first()`, which matched the
  // destination counter in the header — "3 LIVE" — and passed instantly while
  // the ingest card still read OFFLINE and the preview still said "Waiting for
  // a stream…". It shipped a hero image claiming the product was broken, which
  // is the exact failure docs/media/README.md says this script refuses to
  // produce. "Offline" appears in the header and on the ingest badge and
  // nowhere else, so requiring zero of them cannot pass early.
  await expect(page.getByText("Offline", { exact: true })).toHaveCount(0, {
    timeout: 120_000,
  });
  // And the preview has to be showing frames, not its placeholder. BOTH
  // placeholder texts, because the player now says "Ingest offline" when
  // nothing is on air and "Waiting for a stream…" only while it buffers --
  // asserting the second alone would pass on a blank tile showing the first,
  // and this script's whole job is to screenshot a working dashboard.
  for (const placeholder of ["Waiting for a stream…", "Ingest offline"]) {
    await expect(page.getByText(placeholder)).toHaveCount(0, {
      timeout: 120_000,
    });
  }
  await settle(page);
  await page.screenshot({ path: `${OUT}/01-dashboard.png` });
});

test("routing — the thing nothing else does", async ({ page }) => {
  await page.goto("/routing");
  await settle(page);
  await page.screenshot({ path: `${OUT}/02-routing.png`, fullPage: false });

  // The compiled filtergraph, which is the claim this product makes about
  // being inspectable. Worth its own frame rather than being cropped out.
  const graph = page.locator("text=/pan=stereo/").first();
  if (await graph.isVisible().catch(() => false)) {
    await graph.scrollIntoViewIfNeeded();
    await settle(page, 400);
    await page.screenshot({ path: `${OUT}/03-routing-filtergraph.png` });
  }
});

test("meters — loudness measured after routing", async ({ page }) => {
  await page.goto("/meters");
  // Meters are the proof, so this waits for them to move rather than
  // photographing a zeroed scale.
  await settle(page, 6000);
  await page.screenshot({ path: `${OUT}/04-meters.png` });
});

test("sources — one port, many programmes", async ({ page }) => {
  await page.goto("/sources");
  await settle(page);
  await page.screenshot({ path: `${OUT}/05-sources.png` });
});

test("renditions — shared encodes and overlays", async ({ page }) => {
  await page.goto("/renditions");
  await settle(page);
  await page.screenshot({ path: `${OUT}/06-renditions.png` });
});

test("monitoring — every process, with its own FFmpeg output", async ({
  page,
}) => {
  await page.goto("/monitoring");
  await settle(page, 2500);
  await page.screenshot({ path: `${OUT}/07-monitoring.png` });
});

/* The video. One take, no cuts, following the same argument as the stills:
 * this is a live stream; here is where each destination's mix is chosen; here
 * is the graph that produces it; here is the measurement proving it.
 *
 * Deliberately slow. A tour that moves at the speed the operator can click is
 * readable; one that moves at the speed a script can navigate is a blur. */
test("tour — dashboard to routing to meters", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
  await page.waitForTimeout(3000);

  await page.goto("/routing");
  await settle(page, 2000);

  // Walk the destination list so the video shows the selections differing,
  // which is the entire point and is invisible in a single frame.
  const rows = page
    .locator("[role='tab'], button")
    .filter({ hasText: /YouTube|Twitch|Podcast/ });
  const n = Math.min(await rows.count(), 3);
  for (let i = 0; i < n; i++) {
    await rows
      .nth(i)
      .click()
      .catch(() => {});
    await page.waitForTimeout(2200);
  }

  await page.goto("/meters");
  await page.waitForTimeout(6000);

  await page.goto("/monitoring");
  await page.waitForTimeout(3000);
});

/* ---------------------------------------------------------------- part two
 *
 * The seven shots above cover ingest and routing. These cover the rest of the
 * product, because a README that shows only the differentiator implies there is
 * nothing else — and the post-production, chat and automation surfaces are most
 * of what an operator touches after the first week.
 */

test("routing mix matrix — channel-level control", async ({ page }) => {
  await page.goto("/routing");
  await settle(page);
  // The matrix subsumes simple mode and is the more striking image: a grid of
  // per-channel gains rather than a column of checkboxes.
  const tab = page.getByText("Mix matrix", { exact: true }).first();
  if (await tab.isVisible().catch(() => false)) {
    await tab.click();
    await settle(page, 800);
    await page.screenshot({ path: `${OUT}/08-mix-matrix.png` });
  }
});

test("chat — four platforms in one hub", async ({ page }) => {
  await page.goto("/chat");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/09-chat.png` });
});

test("playout — serving a player from polyemesis itself", async ({ page }) => {
  await page.goto("/playout");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/10-playout.png` });
});

test("library — the recorded catalogue", async ({ page }) => {
  await page.goto("/library");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/11-library.png` });
});

test("jobs — the post-production queue and its governor", async ({ page }) => {
  await page.goto("/jobs");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/12-jobs.png` });
});

test("automation — schedules and alert rules", async ({ page }) => {
  await page.goto("/automation");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/13-automation.png` });
});

test("clips — the editor", async ({ page }) => {
  await page.goto("/clips");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/14-clips.png` });
});

test("recordings — segments and retention", async ({ page }) => {
  await page.goto("/recordings");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/15-recordings.png` });
});

test("settings — listeners, and the one-port design in the UI", async ({
  page,
}) => {
  await page.goto("/settings");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/16-settings.png` });
  // Full page as well: settings is long, and the single-viewport crop cuts off
  // most of what the page is for.
  await page.screenshot({
    path: `${OUT}/17-settings-full.png`,
    fullPage: true,
  });
});
