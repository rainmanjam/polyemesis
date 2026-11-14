import { expect } from "@playwright/test";
import { mkdirSync, readFileSync, renameSync, rmSync } from "node:fs";
import { basename } from "node:path";

import {
  destinationsSettled,
  loudnessRowsFromFeed,
  secretsFromEnv,
  progressFromStatus,
  shootProblems,
  type SecretEntry,
} from "../src/lib/captureGuards";
import { allowedFailedWords } from "./shotManifest";

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

/** Which scene this run photographs: "full" (the whole demo seed) or "sports"
 *  (one programme, five live destinations). docs/media/shots.json says which
 *  scene owns which shot. */
export const SCENE: "full" | "sports" = process.env.SHOT_SCENE === "sports" ? "sports" : "full";

/* THE REFUSALS. The decisions are in ui/src/lib/captureGuards.ts, where vitest
 * measures them; what is here only reads the page, the API and the secrets
 * file and hands the result over. */

let secretEntries: SecretEntry[] | undefined;

/** The rig's secrets (`kind<TAB>value` per line) from the file named by
 *  SHOT_SECRETS_FILE, read once. Required in every scene; `none` says a run has
 *  nothing to check. The decisions are in captureGuards.ts. */
function secrets(): SecretEntry[] {
  return (secretEntries ??= secretsFromEnv(process.env.SHOT_SECRETS_FILE, (f) => readFileSync(f, "utf8")));
}

/** Throws unless the page, as it is NOW, may be photographed as `file`: dark
 *  theme, no secret legible, no failed-state word the shot does not declare.
 *  The message names the file and the kinds or words, never a value. */
export async function assertShootable(page: import("@playwright/test").Page, file: string) {
  const entries = secrets();
  const facts = await page.evaluate(() => ({
    theme: document.documentElement.getAttribute("data-theme"),
    visibleText: [
      document.body.innerText,
      ...Array.from(document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input:not([type=password]):not([type=hidden]), textarea")).map(
        (el) => el.value,
      ),
    ].join("\n"),
  }));
  const problems = shootProblems({ ...facts, secrets: entries, allow: allowedFailedWords(file) });
  if (problems.length > 0) throw new Error(`${file}: refusing to photograph this page: ${problems.join("; ")}`);
}

/** A screenshot into OUT, after assertShootable. For the shots that are not
 *  taken through shoot() because they have no placeholder to wait out.
 *
 *  IN THE SPORTS SCENE the destinations are checked again here: waitSettled
 *  runs once at the start of a test, and a shot's own waits (meters: 6 s, up to
 *  90 s for the analyser and 60 s for every track) put the shutter up to a
 *  minute and a half later. A destination that fell below 0.97x in that gap
 *  would be photographed in amber. The check is before the shutter (waiting up
 *  to 30 s for a reading that is only borderline, such as 0.969x, to come back)
 *  and AFTER it, where a frame that is no longer settled is thrown away and
 *  shot again, up to four times; past that the shot fails naming the
 *  destination. No unsettled frame is ever written.
 *
 *  `opts.frameCheck` is for a property of the frame that comes and goes (a
 *  commentary voice between bursts): it runs after the shutter, a problem
 *  leaves no file, and the problem is returned so the caller can try again.
 *  Returns null when the file was written. */
export async function snap(
  page: import("@playwright/test").Page,
  file: string,
  opts: {
    clip?: { x: number; y: number; width: number; height: number };
    fullPage?: boolean;
    frameCheck?: () => Promise<string | null>;
  } = {},
): Promise<string | null> {
  const { frameCheck, ...shotOpts } = opts;
  await assertShootable(page, file);
  let unsettled = "";
  for (let attempt = 1; attempt <= 4; attempt++) {
    await waitStillSettled(page, file);
    unsettled = "";
    const problem = await shutter(page, `${OUT}/${file}`, shotOpts, async () => {
      await assertShootable(page, file);
      if (SCENE === "sports") {
        const verdict = await settledVerdict(page, SETTLED_UPTIME_SEC);
        if (!verdict.ok) {
          unsettled = verdict.why;
          return `not settled: ${verdict.why}`;
        }
      }
      return frameCheck ? frameCheck() : null;
    });
    if (unsettled === "") return problem;
    console.warn(`re-shooting ${file}: the destinations were no longer settled at the shutter (${unsettled})`);
  }
  throw new Error(`${file}: the destinations were no longer settled at the shutter in 4 attempts: ${unsettled}`);
}

/** Sports scene only: waits up to 30 s for every destination to be settled
 *  (the same verdict as waitSettled), then throws naming the reason. */
async function waitStillSettled(page: import("@playwright/test").Page, file: string) {
  if (SCENE !== "sports") return;
  const deadline = Date.now() + 30_000;
  for (;;) {
    const verdict = await settledVerdict(page, SETTLED_UPTIME_SEC);
    if (verdict.ok) return;
    if (Date.now() >= deadline) {
      throw new Error(`${file}: the destinations are not settled at the shutter: ${verdict.why}`);
    }
    await page.waitForTimeout(1000);
  }
}

/** Writes the PNG under a temporary name beside the target and renames it into
 *  place only if `check` finds nothing wrong after the shutter. A refused frame
 *  therefore never exists under its real name, where the post-capture check
 *  (scripts/check-shots.mjs) would count it as fresh. Returns what `check`
 *  found; a throw from `check` leaves no file either. */
async function shutter(
  page: import("@playwright/test").Page,
  path: string,
  shotOpts: { clip?: { x: number; y: number; width: number; height: number }; fullPage?: boolean },
  check: () => Promise<string | null>,
): Promise<string | null> {
  const partial = path.replace(/\.png$/, ".partial.png");
  try {
    await page.screenshot({ path: partial, ...shotOpts });
    const problem = await check();
    if (problem === null) renameSync(partial, path);
    return problem;
  } finally {
    rmSync(partial, { force: true });
  }
}

/** Blocks until every enabled destination has run for minUptimeSec at a steady
 *  speed with nothing dropped and its loudness is integrated and passing. A
 *  shot taken earlier shows 0.84x in amber and no integrated loudness. Polls
 *  GET /api/v1/status and GET /api/v1/loudness every 2 s for up to 120 s. */
export async function waitSettled(page: import("@playwright/test").Page, minUptimeSec = SETTLED_UPTIME_SEC) {
  const deadline = Date.now() + 120_000;
  let why = "never polled";
  for (;;) {
    const verdict = await settledVerdict(page, minUptimeSec);
    if (verdict.ok) return;
    why = verdict.why;
    if (Date.now() >= deadline) break;
    await page.waitForTimeout(2000);
  }
  throw new Error(`destinations did not settle within 120 s: ${why}`);
}

/** How long a destination must have run before a sports shot may show it. */
const SETTLED_UPTIME_SEC = 30;

async function settledVerdict(page: import("@playwright/test").Page, minUptimeSec: number) {
  const [status, loudness] = await Promise.all([
    page.request.get("/api/v1/status").then((r) => r.json()),
    page.request.get("/api/v1/loudness").then((r) => r.json()),
  ]);
  return destinationsSettled(progressFromStatus(status), loudnessRowsFromFeed(loudness), minUptimeSec);
}

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
  /** rend.emptyTitle ("No renditions yet") and rend.empty ("No renditions. ...") */
  renditions: "No renditions",
  /** rec.empty */
  recordings: "No recordings yet.",
  /** lib.nothingRecorded */
  library: "Nothing recorded yet.",
  /** meters.emptyTitle (the EmptyState title, app-council A83) */
  meters: "Nothing to measure yet",
  /** dash.noDestinationsTitle (the EmptyState title, app-council A83) */
  destinations: "No destinations yet",
  /** route.noDestinations, the EmptyState body on the Routing page. */
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
  await waitForIngestChip(page);
  await page.waitForTimeout(ms);
}

/** The header's ingest chip has no reading until the page's first ingest
 *  frames arrive. It used to label that gap "Stopped", like a stopped process,
 *  and a page that does not wait for live data of its own (Sources,
 *  Renditions, Routing, Settings) was photographed in it, with "Ingest
 *  Stopped" beside a red "3 LIVE" tally. The chip now says "Loading…" there
 *  instead, which is true but is still not the picture: wait for it to leave
 *  both. If it never does, the shot goes ahead and shows it, because then it
 *  is the truth. */
async function waitForIngestChip(page: import("@playwright/test").Page) {
  const chip = page.getByTestId("chrome-ingest-status");
  // Attached first. Straight after a navigation the chrome has often not
  // rendered yet, and a count of zero then skipped the wait entirely: the
  // chat shot was taken 1.5 s in, with the chip still reading "Loading…".
  await chip.waitFor({ state: "attached", timeout: 10_000 }).catch(() => {});
  if ((await chip.count()) === 0) return;
  await expect(chip).not.toHaveText(/^(Stopped|Loading…)$/, { timeout: 15_000 }).catch(() => {});
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

    // Light, a legible secret or a failure on screen will not mend by waiting:
    // thrown, not retried.
    await assertShootable(page, basename(path));

    // And still true after the shutter. A screenshot is not instantaneous, and
    // the frame that matters is the one on disk -- so it is checked BEFORE the
    // file takes its real name.
    const broke = await shutter(page, path, shotOpts, async () => {
      const b = await present();
      if (b === null) await assertShootable(page, basename(path));
      return b;
    });
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

