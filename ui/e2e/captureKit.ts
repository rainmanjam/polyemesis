import { expect } from "@playwright/test";
import { mkdirSync } from "node:fs";

/* The capture harness's shared machinery, used by every media-capture spec.
 *
 * EXTRACTED RATHER THAN COPIED. Every function below is the residue of a
 * screenshot that shipped wrong -- a hero image reading OFFLINE, a black tile
 * saying "Waiting for a stream...", a meters page with a dead track. A second
 * capture spec that reimplemented "wait, then shoot" would not inherit any of
 * that, and the first bad frame it produced would be one this file already
 * knows how to refuse. The comments travel with the code for the same reason:
 * each one names the specific failure its line prevents, and a reader deciding
 * whether a wait is really necessary needs the incident, not the rule.
 */

/* docs/media is where these have always gone and where docs/media/README.md
 * says they live. Overridable because a change to the capture harness has to be
 * verifiable without overwriting the committed artefacts to prove it worked --
 * a run that fails halfway then leaves the repository holding a mixture of old
 * and new shots, and nothing says which is which. scripts/demo-seed.sh passes
 * this through. */
export const OUT = process.env.SHOT_DIR ?? "../docs/media";
mkdirSync(OUT, { recursive: true });

/* WAITING FOR REAL STATE, EXPRESSED AS THE ABSENCE OF THE EMPTY STATE.
 *
 * The dashboard shot below already works this way and the comment there says
 * why: asserting the PRESENCE of something live matches the first element that
 * happens to say "live", which on this product is a header counter that reads
 * "3 LIVE" while every card underneath is still dark. The empty state has the
 * opposite property -- there is exactly one of it, it is the whole page, and it
 * cannot coexist with content.
 *
 * These are the empty-state sentences from ui/src/lib/i18n/en.json, quoted
 * exactly and labelled with the key they came from. THAT LABEL IS THE WHOLE
 * SAFETY: a reworded sentence makes the guard match nothing, which passes
 * instantly and photographs the empty page it was meant to prevent. The key is
 * what makes the connection findable from the other end, when someone edits the
 * translation and wonders what else says this. */
export const EMPTY = {
  /** empty.sourcesTitle */
  sources: "No sources yet",
  /** rend.empty */
  renditions: "No renditions.",
  /** rec.empty */
  recordings: "No recordings yet.",
  /** lib.nothingRecorded */
  library: "Nothing recorded yet.",
  /** meters.nothingToMeasure */
  meters: "Nothing to measure yet.",
  /** dash.noDestinations */
  destinations: "No destinations yet.",
  /** RoutingPage.tsx, not a translation key: the page renders this literal. */
  routing: "Add a destination first",
} as const;

/** Blocks until a page is showing content rather than its empty state.
 *
 *  A screenshot of an empty state is not a screenshot of a broken product, and
 *  that is exactly the problem: it looks deliberate. Every shot in this file
 *  used to be taken as soon as the route rendered, which against the only
 *  installs available -- one source, no destinations, no recordings -- produced
 *  a set of images advertising a product with nothing in it. */
export async function populated(page: import("@playwright/test").Page, empty: string) {
  await expect(page.getByText(empty, { exact: false })).toHaveCount(0, {
    timeout: 60_000,
  });
}

/** Scrolls a locator to the TOP of the viewport, so the shot is framed on it.
 *
 *  scrollIntoViewIfNeeded is the obvious call and is the wrong one here: it
 *  scrolls the minimum distance that makes the element visible, so an element
 *  already at the bottom edge stays at the bottom edge and the frame is
 *  unchanged. These are photographs; what is at the top of the frame is the
 *  subject. */
export async function topOfFrame(
  page: import("@playwright/test").Page,
  target: import("@playwright/test").Locator,
) {
  await expect(target).toBeVisible({ timeout: 30_000 });
  await target.evaluate((el) => el.scrollIntoView({ block: "start", behavior: "instant" }));
  await page.waitForTimeout(300);
}

/** Settles layout before a shot: fonts loaded, no animation mid-flight.
 *
 *  Without the font wait, the first screenshot of a run catches Inter still
 *  swapping and every glyph shifts a pixel between runs — which turns a diff of
 *  two captures into noise. */
export async function settle(page: import("@playwright/test").Page, ms = 900) {
  await dismissBanners(page);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}

/** Photograph a page ONLY IF the frame is still true AT THE SHUTTER.
 *
 *  The hero shot waited for "Waiting for a stream…" to disappear, then ran
 *  populated() and settle() -- up to a minute of further waiting -- and only
 *  then fired. The preview idle-stops after thirty seconds with no viewer, so
 *  the placeholder came back in the gap and the shot shipped a black rectangle
 *  reading "Waiting for a stream…": the exact image the guard above it exists
 *  to prevent, and the second time this harness has produced one.
 *
 *  The guard was never wrong. It was checking a different second from the one
 *  it photographed, which is a check-then-act gap and not a weak assertion --
 *  no timeout on the assertion could have closed it.
 *
 *  So the check moves to the shutter and REPEATS AFTER IT. A frame that went
 *  wrong while the file was being written is thrown away and retried rather
 *  than kept, and a page that cannot hold the frame fails the capture instead
 *  of quietly writing a picture of the product looking broken.
 */
export type Forbidden = string | { text: string; exact: boolean };

export async function shoot(
  page: import("@playwright/test").Page,
  path: string,
  forbid: Forbidden[],
  opts: { fullPage?: boolean; settleMs?: number } = {},
) {
  const { settleMs = 900, ...shotOpts } = opts;
  // EXACTNESS IS PER ENTRY, and getting it wrong costs a two-minute timeout.
  //
  // Playwright's getByText matches a case-insensitive SUBSTRING by default, so
  // "Offline" also matches "Ingest offline" and anything else containing it.
  // The hero's original guard passed { exact: true } for that reason and I
  // dropped it when folding the check in here -- on a two-programme install
  // there is simply more text on the page, the locator started matching
  // something that is always present, and the shot failed after four attempts
  // at a page that was perfectly fine. The placeholders stay substring matches,
  // which is how they were written.
  const locate = (f: Forbidden) =>
    typeof f === "string"
      ? page.getByText(f)
      : page.getByText(f.text, { exact: f.exact });
  const label = (f: Forbidden) => (typeof f === "string" ? f : f.text);

  const present = async () => {
    for (const f of forbid) {
      if ((await locate(f).count()) > 0) return label(f);
    }
    return null;
  };

  for (let attempt = 1; attempt <= 4; attempt++) {
    for (const f of forbid) {
      await expect(locate(f)).toHaveCount(0, { timeout: 120_000 });
    }
    await settle(page, settleMs);

    // Still true now, after the settle? Counted rather than awaited: this is a
    // question about THIS instant, and waiting for it would reopen the gap.
    if (await present()) continue;

    await page.screenshot({ path, ...shotOpts });

    // And still true after the shutter. A screenshot is not instantaneous, and
    // the frame that matters is the one on disk.
    const broke = await present();
    if (!broke) return;
    console.warn(`re-shooting ${path}: "${broke}" reappeared during the capture`);
  }
  throw new Error(
    `${path}: could not hold a live frame across the shutter after 4 attempts. ` +
      `Refusing to write a screenshot showing ${forbid.map((f) => JSON.stringify(label(f))).join(" or ")} ` +
      `-- that is a picture of the product looking broken, which is what this ` +
      `harness exists not to produce.`,
  );
}

/** Closes the dismissible chrome that spans the top of every page.
 *
 *  The update banner reads "Development build dev. Updates are not offered for
 *  builds from source", which is TRUE of what this photographs and is a
 *  sentence about the build rather than about the product. Left alone it takes
 *  the top of all eighteen frames and says nothing about any of them.
 *
 *  Clicked rather than suppressed with CSS, and clicked in every shot rather
 *  than once: `dismissed` is component state, and Playwright gives each test a
 *  fresh page, so it comes back on every navigation. The first-run tour banner
 *  underneath it is handled server-side instead -- scripts/demo_seed_driver.go
 *  marks the tour complete, because that one IS persisted. */
export async function dismissBanners(page: import("@playwright/test").Page) {
  const close = page.getByRole("button", { name: "Dismiss" });
  for (let i = await close.count(); i > 0; i--) {
    await close.first().click({ timeout: 2000 }).catch(() => {});
  }
}

/** Hides anything that changes between runs but says nothing about the product,
 *  so re-capturing produces a comparable image rather than a different one. */
export async function calm(page: import("@playwright/test").Page) {
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

