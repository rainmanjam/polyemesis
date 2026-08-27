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

/* docs/media is where these have always gone and where docs/media/README.md
 * says they live. Overridable because a change to the capture harness has to be
 * verifiable without overwriting the committed artefacts to prove it worked --
 * a run that fails halfway then leaves the repository holding a mixture of old
 * and new shots, and nothing says which is which. scripts/demo-seed.sh passes
 * this through. */
const OUT = process.env.SHOT_DIR ?? "../docs/media";
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
const EMPTY = {
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
async function populated(page: import("@playwright/test").Page, empty: string) {
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
async function topOfFrame(
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
async function settle(page: import("@playwright/test").Page, ms = 900) {
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
type Forbidden = string | { text: string; exact: boolean };

async function shoot(
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
async function dismissBanners(page: import("@playwright/test").Page) {
  const close = page.getByRole("button", { name: "Dismiss" });
  for (let i = await close.count(); i > 0; i--) {
    await close.first().click({ timeout: 2000 }).catch(() => {});
  }
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
  // BEFORE the placeholder guard below, not after it. Waiting for the
  // destinations to populate took up to a minute, and it used to sit between
  // that guard and the shutter -- which is most of how the preview had time to
  // idle out and put its placeholder back.
  await populated(page, EMPTY.destinations);
  // And the preview has to be showing frames, not its placeholder. BOTH
  // placeholder texts: the player says "Ingest offline" when nothing is on air
  // and "Waiting for a stream…" only while it buffers, so asserting the second
  // alone would pass on a blank tile showing the first.
  await shoot(page, `${OUT}/01-dashboard.png`, [
    "Waiting for a stream…",
    "Ingest offline",
    // Exact: the standalone ingest badge, not every string containing the word.
    { text: "Offline", exact: true },
  ]);

  // The fan-out, framed on its own.
  //
  // THE SHOT ABOVE IS THE TOP OF THE PAGE AND THE TOP OF THE PAGE IS THE
  // INGEST CARD, which reports OFFLINE with no bitrate while 200 MB of video
  // is demonstrably on the relay. That is not this harness getting it wrong:
  // engine.Status() fills `ingest` from the ingest CHILD PROCESS, and under the
  // one-port design there is no such process -- the Go SRT listener delivers
  // straight to the hub. AppLayout already computes liveness from the relay
  // instead and says "Live" in the header of the same screenshot; the
  // dashboard card does not. web/src/pages/features.astro has been omitting
  // the dashboard capture over exactly this.
  //
  // So the differentiator gets a frame that tells the truth: thirteen
  // destinations across five platforms and three rendition tiers, which is the
  // claim that is actually being made and the one every card in it supports.
  // Delete this shot, not that one, when the ingest card is fixed.
  // The heading, by role and prefix. Its text is "Destinations" followed by the
  // count in a sibling span, so an exact text match on the word alone matches
  // nothing at all -- and a locator that matches nothing waits out its timeout
  // rather than saying so.
  await topOfFrame(page, page.getByRole("heading", { name: /^Destinations/ }));
  // The preview tile sits inside this frame and re-mounts on the scroll, so
  // the placeholder the hero shot waited out reappears for a moment. Waited
  // again HERE rather than trusted from thirty lines up: the preview also
  // idle-stops after thirty seconds with no viewer
  // (settings.preview.idleTimeoutSeconds), which makes "it was playing
  // earlier" a claim about a different second.
  await shoot(page, `${OUT}/18-destinations.png`, ["Waiting for a stream…", "Ingest offline"], {
    settleMs: 600,
  });
});

/* TWO PROGRAMMES, WHICH IS A DIFFERENT PAGE.
 *
 * Dashboard.tsx divides the destination area into per-programme lanes and
 * badges each card with the programme it carries -- and does NEITHER with one
 * source, which its own comment calls "the shape this page had before
 * per-source anything". Every shot this harness took before now was of that
 * degenerate case, so the scoping work that most of #497/#515/#540 exists for
 * was invisible in every image on the site.
 *
 * Both lanes are genuinely live: seed_demo.go creates the second programme
 * with its own destinations and capture-media.sh publishes a second stream to
 * it, on the SAME SRT port with a different streamid -- the one-port design
 * demonstrated rather than described. A second lane reading "Offline" beside a
 * live one would argue that multi-source does not work.
 */
test("dashboard — two programmes, and the lanes that divide them", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();

  // The lane headings are the subject. If only one programme exists they are
  // not rendered at all, so their absence means the seed did not take -- and a
  // shot of a single-programme dashboard filed as the multi-programme one is
  // worse than no shot.
  const laneHeading = page.getByText("Studio B — panel show").first();
  await expect(
    laneHeading,
    "no lane heading for the second programme, so this is a single-programme " +
      "dashboard being photographed under a multi-programme name",
  ).toBeVisible({ timeout: 120_000 });

  await topOfFrame(page, page.getByRole("heading", { name: /^Destinations/ }));
  await shoot(page, `${OUT}/19-lanes.png`, ["Waiting for a stream…", "Ingest offline"], {
    settleMs: 700,
  });
});

test("routing — the thing nothing else does", async ({ page }) => {
  await page.goto("/routing");
  await populated(page, EMPTY.routing);
  // Track rows are drawn from the PROBED layout, so a routing shot taken
  // before the probe lands is a mixer with no channels in it -- a page that
  // renders perfectly and demonstrates nothing.
  await expect(page.getByText("no signal")).toHaveCount(0, { timeout: 60_000 });
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
  // A meter reads per RUNNING destination, so this page is empty until one is
  // actually publishing -- and an empty meters page is the single most
  // misleading image this file can produce: the whole claim of the product is
  // that it measures what it sends.
  await populated(page, EMPTY.meters);
  // Meters are the proof, so this waits for them to move rather than
  // photographing a zeroed scale. Six seconds is one full EBU R128 short-term
  // window plus the analyser's own start-up.
  await settle(page, 6000);

  // AND THE COMPLIANCE BLOCK HAS TO HAVE ANSWERED, which six seconds does not
  // buy: integrated loudness needs about twenty seconds of programme before it
  // means anything, and the page says so in its own words while showing
  // "NOT UPDATING". A shot taken before then has no per-destination figures in
  // it at all -- and web/src/pages/index.astro quotes three of them in body
  // copy and alt text as the whole of its proof section. Waiting on the badge
  // rather than on another sleep, because the analyser's start-up is not a
  // fixed cost.
  await expect(
    page.getByText("NOT UPDATING"),
    "the loudness analyser never reported, so this shot has no per-destination " +
      "figures and the front page's proof section quotes numbers that are not in it",
  ).toHaveCount(0, { timeout: 90_000 });
  // AND EVERY TRACK IS METERING.
  //
  // The guards above cover the compliance block and nothing else, so a shot
  // could be -- and was, twice -- taken with a track reading "no signal on this
  // track" while the other two drew bars. On a page whose subtitle is "every
  // channel of every ingest track" that is the one thing the image must not
  // show, and it is what the front page's alt text used to claim was happening.
  //
  // Refusing is the point. If a track genuinely has no audio the capture stops
  // rather than publishing a picture of a half-working meter, which is the same
  // bargain the ingest-live guard makes at the top of this file.
  await expect(
    page.getByText("no signal on this track"),
    "a track is not metering, so this shot shows the meters page half working. " +
      "Either the ingest is not carrying that track or the level feed is not " +
      "reporting it -- see #613. Photographing it either way tells a reader the " +
      "product does not measure what it says it measures",
  ).toHaveCount(0, { timeout: 60_000 });

  await settle(page, 800);
  await page.screenshot({ path: `${OUT}/04-meters.png` });
});

test("sources — one port, many programmes", async ({ page }) => {
  await page.goto("/sources");
  await populated(page, EMPTY.sources);
  // MORE THAN ONE, which is the entire claim of the page. One source renders
  // the same layout and is indistinguishable in a still image from any
  // single-ingest competitor, so "not empty" is not a strong enough guard here.
  //
  // Counted on the publish-URL block rather than on a card: there is exactly
  // one of those per source whatever state it is in, whereas the URL rows
  // themselves are one per protocol and would reach two on a single source.
  const perSource = page.locator('[data-tour="source-publish-urls"]');
  await expect.poll(() => perSource.count(), { timeout: 30_000 }).toBeGreaterThan(1);
  await maskPublishTokens(page);
  await settle(page);
  await page.screenshot({ path: `${OUT}/05-sources.png` });
  await assertNoTokenSurvived(page);
});

/** Replaces every publish token on the page with a visibly fake one.
 *
 *  THIS PAGE SHOWS A LIVE CREDENTIAL IN PLAINTEXT, deliberately and correctly:
 *  a source's token is what the operator pastes into OBS, and the card says so
 *  in as many words. It is the one thing on any of these seventeen screens that
 *  must not survive into a committed PNG.
 *
 *  The tokens photographed here belong to an install that exists for one
 *  capture and is deleted afterwards, so nothing is at risk today. The reason
 *  to mask anyway is the same reason this repository refuses realistic-looking
 *  fixtures: a marketing screenshot of a credential is a screenshot somebody
 *  copies the habit from, and the first time this script is pointed at a real
 *  install with --base it would publish a real one. See the advisory on stream
 *  keys in SECURITY.md.
 *
 *  Read from the API rather than pattern-matched: a regex for "a base64url run
 *  of about 32 characters" also matches a session id, a rendition hash and
 *  anything else the page happens to render, and a mask that eats unrelated
 *  text is a mask nobody can review.
 *
 *  REAPPLIED ON EVERY MUTATION, not applied once. This page subscribes to live
 *  telemetry -- the RTT and loss figures beside PUBLISHING update about once a
 *  second -- so a single pass is undone by the next render, and the window
 *  between the mask and the shutter is exactly where the credential comes
 *  back. */
async function maskPublishTokens(page: import("@playwright/test").Page) {
  const masked = await page.evaluate(async () => {
    const res = await fetch("/api/v1/sources", { credentials: "same-origin" });
    if (!res.ok) throw new Error(`GET /api/v1/sources failed (${res.status})`);
    const tokens = ((await res.json()) as { token?: string }[])
      .map((s) => s.token)
      .filter((t): t is string => !!t && t.length > 8);

    const fake = "DEMO-TOKEN-NOT-A-REAL-CREDENTIAL";
    let hits = 0;
    const swap = (text: string) => {
      let out = text;
      for (const t of tokens) {
        while (out.includes(t)) {
          out = out.replace(t, fake);
          hits++;
        }
      }
      return out;
    };
    // WRITTEN BACK ONLY WHEN IT CHANGED, and that is what stops this from
    // spinning: assigning a text node its own value still queues a
    // characterData record, so an unconditional write inside an observer that
    // is watching characterData re-triggers itself for ever.
    const sweep = () => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        if (!n.nodeValue) continue;
        const next = swap(n.nodeValue);
        if (next !== n.nodeValue) n.nodeValue = next;
      }
      // Inputs carry their value as a property, not as text, so the walk above
      // cannot see the TOKEN field -- which is the one that matters most.
      for (const el of Array.from(document.querySelectorAll("input, textarea"))) {
        const field = el as HTMLInputElement | HTMLTextAreaElement;
        if (!field.value) continue;
        const next = swap(field.value);
        if (next !== field.value) field.value = next;
      }
    };

    sweep();
    new MutationObserver(sweep).observe(document.body, {
      subtree: true,
      childList: true,
      characterData: true,
    });
    return { tokens: tokens.length, hits };
  });

  // Asserted, not assumed. A mask that silently matched nothing -- the field
  // moved, the API shape changed -- leaves the credential on screen and reports
  // success, which is the failure this whole function exists to prevent.
  expect(masked.tokens, "no publish tokens came back from the API").toBeGreaterThan(0);
  expect(masked.hits, "the tokens were never found on the page to mask").toBeGreaterThan(0);
}

/** Reads the page back AFTER the shutter and fails if a token is on it.
 *
 *  The check has to happen on the far side of the screenshot. Checking before
 *  it proves the mask worked at some earlier instant, which is not the claim
 *  that matters: the claim is about the pixels that were written to disk. */
async function assertNoTokenSurvived(page: import("@playwright/test").Page) {
  const survivors = await page.evaluate(async () => {
    const res = await fetch("/api/v1/sources", { credentials: "same-origin" });
    const tokens = ((await res.json()) as { token?: string }[])
      .map((s) => s.token)
      .filter((t): t is string => !!t && t.length > 8);
    const haystack =
      (document.body.innerText ?? "") +
      Array.from(document.querySelectorAll("input, textarea"))
        .map((el) => (el as HTMLInputElement).value ?? "")
        .join("\n");
    return tokens.filter((t) => haystack.includes(t)).length;
  });
  expect(survivors, "a publish token was still on screen when the shot was taken").toBe(0);
}

test("renditions — the ladder, shared across destinations", async ({ page }) => {
  await page.goto("/renditions");
  // The ladder is the shot. Without renditions this page is one paragraph
  // explaining that passthrough is fine, which is true and photographs as a
  // feature that does not exist.
  await populated(page, EMPTY.renditions);
  await settle(page);
  await page.screenshot({ path: `${OUT}/06-renditions.png` });
});

test("monitoring — every process, with its own FFmpeg output", async ({ page }) => {
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
  const rows = page.locator("[role='tab'], button").filter({ hasText: /YouTube|Twitch|Podcast/ });
  const n = Math.min(await rows.count(), 3);
  for (let i = 0; i < n; i++) {
    await rows.nth(i).click().catch(() => {});
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
  await populated(page, EMPTY.routing);
  await settle(page);
  // SELECT A DESTINATION THAT IS ALREADY IN MATRIX MODE FIRST.
  //
  // Clicking the tab on whichever destination happens to be selected — the
  // first in the list, which is in simple mode — SWITCHES ITS MODE. What that
  // photographs is a grid of zeros with an UNSAVED badge in the corner: a
  // striking picture of a control nobody has used, on a destination this
  // capture has just dirtied. The matrix is worth showing with numbers in it.
  //
  // Found through the API rather than by name, so this keeps working against
  // an install that is not this fixture. Matched on a prefix because the list
  // truncates long names with an ellipsis.
  const matrixName = await page.evaluate(async () => {
    const res = await fetch("/api/v1/destinations", { credentials: "same-origin" });
    if (!res.ok) return null;
    const rows = (await res.json()) as { destination?: { name?: string; profile?: { mode?: string } } }[];
    return rows.find((r) => r.destination?.profile?.mode === "matrix")?.destination?.name ?? null;
  });
  if (!matrixName) {
    // Nothing to photograph, and saying so beats writing a picture of an empty
    // grid that looks exactly like the feature not working.
    test.skip(true, "no destination on this install uses the mix matrix");
  }
  await page.getByText(matrixName!.slice(0, 12), { exact: false }).first().click();
  await settle(page, 500);

  // The matrix subsumes simple mode and is the more striking image: a grid of
  // per-channel gains rather than a column of checkboxes.
  const tab = page.getByText("Mix matrix", { exact: true }).first();
  await expect(tab).toBeVisible({ timeout: 15_000 });
  await tab.click();
  await settle(page, 800);
  await page.screenshot({ path: `${OUT}/08-mix-matrix.png` });
});

/* THE ONE STUBBED SHOT, AND IT IS DECLARED RATHER THAN QUIET.
 *
 * Everything else in this file photographs a real polyemesis doing real work:
 * a real ingest, real destinations, real bytes. Chat cannot be. A message
 * arrives only from a platform account attached to the install, which needs
 * live OAuth credentials for somebody's real channel -- and a screenshot of
 * real viewers' messages is neither reproducible nor theirs to publish.
 *
 * So the timeline below is fixture data served to the REAL page. What is being
 * photographed is genuine: the merge into one timeline, the per-platform
 * attribution, the send box that knows which platforms can currently accept a
 * message. What is fabricated is only who said it.
 *
 * Without this the shot was a photograph of the empty state -- "Chat is
 * running but no platform account is attached yet" -- under a test named "four
 * platforms in one hub". The picture argued the opposite of its own title.
 *
 * Recorded in docs/media/README.md as the single exception to "generated, not
 * hand-made", because an undeclared exception is how a policy stops meaning
 * anything.
 */
const CHAT_PLATFORMS = ["youtube", "twitch", "facebook", "kick"] as const;

const DEMO_CHAT = [
  ["twitch", "hexcode", "wait, one ingest and it fans out to all four?"],
  ["youtube", "mira_dev", "the per-destination audio thing is the bit I want"],
  ["kick", "nightowl_", "so the VOD track drops the music bed automatically"],
  ["facebook", "Dana Whitfield", "audio is noticeably cleaner this week"],
  ["youtube", "sam_tk", "how many destinations can it drive at once?"],
  ["twitch", "brb_afk", "5.5 Mbps and zero dropped frames, that's the whole point"],
  ["kick", "vexcarter", "does moderation reach back to the platform or just hide it here"],
  ["facebook", "Ana Ruiz", "one timeline for four chats is genuinely the feature"],
] as const;

test("chat — four platforms in one hub", async ({ page }) => {
  // Matched on the PATH: the overview is requested as `/chat?limit=300`, and a
  // `**/chat` glob does not match a URL carrying a query string -- which fails
  // as "no messages rendered", several steps from the cause.
  await page.route(
    (url) => url.pathname === "/api/v1/chat",
    (route) =>
      route.fulfill({
        json: {
          configured: true,
          statuses: CHAT_PLATFORMS.map((platform, i) => ({
            platform,
            account: { youtube: "polyemesis", twitch: "polyemesis", facebook: "Polyemesis", kick: "polyemesis" }[platform],
            state: "live",
            channel: { youtube: "polyemesis", twitch: "polyemesis", facebook: "Polyemesis Live", kick: "polyemesis" }[platform],
            canSend: true,
            received: DEMO_CHAT.filter((m) => m[0] === platform).length,
            sent: i === 0 ? 1 : 0,
            restarts: 0,
          })),
          // Newest last, seconds apart, so the timeline reads as a conversation
          // rather than as a burst that all arrived at once.
          messages: DEMO_CHAT.map(([platform, name, text], i) => ({
            id: `demo-${i}`,
            platform,
            account: platform === "facebook" ? "Polyemesis" : "polyemesis",
            channel: platform === "facebook" ? "Polyemesis Live" : "polyemesis",
            author: { id: `u-${name}`, name },
            text,
            at: new Date(Date.now() - (DEMO_CHAT.length - i) * 17_000).toISOString(),
          })),
          limits: CHAT_PLATFORMS.map((platform) => ({
            platform,
            maxChars: platform === "youtube" ? 200 : 500,
          })),
        },
      }),
  );

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
  // Sessions are derived from the index on the recorder's own 30-second scan,
  // so a library shot taken immediately after seeding catches the sweep that
  // has not run yet rather than a catalogue that is empty.
  await populated(page, EMPTY.library);
  // Framed on the catalogue, not on the uploader above it. The Media card is
  // the first thing on this route and reads "Nothing uploaded yet" on any
  // install that has not uploaded anything -- so the default framing gives half
  // the image to an empty state and pushes the sessions below the fold, which
  // is the exact complaint this whole change exists to answer.
  // The count label is PLURALISED, so "sessions" is not a literal that always
  // exists: LibraryPage renders `{n} session{n === 1 ? "" : "s"}`, and a seed
  // that happens to produce exactly one recorded session prints "1 session".
  // This locator was written when the seed made two, and it started timing out
  // the moment the second programme changed how the recorder carves them up --
  // a capture failure that says "element not found" and reads like a broken
  // page rather than a missing letter.
  await topOfFrame(page, page.getByText(/\d+ sessions?\b/).first());
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
  await populated(page, EMPTY.recordings);
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/15-recordings.png` });
});

test("settings — listeners, and the one-port design in the UI", async ({ page }) => {
  await page.goto("/settings");
  await settle(page, 1500);
  await page.screenshot({ path: `${OUT}/16-settings.png` });
  // Full page as well: settings is long, and the single-viewport crop cuts off
  // most of what the page is for.
  await page.screenshot({ path: `${OUT}/17-settings-full.png`, fullPage: true });
});
