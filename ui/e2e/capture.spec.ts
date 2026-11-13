import { expect, test } from "@playwright/test";

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

import {
  EMPTY,
  OUT,
  calm,
  populated,
  settle,
  shoot,
  snap,
  SCENE,
  topOfFrame,
  waitSettled,
} from "./captureKit";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
  await calm(page);
});

test("dashboard — the hero shot", async ({ page }) => {
  // The sports scene is photographed only once every destination has settled.
  if (SCENE === "sports") await waitSettled(page);
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
  // The ingest's not-running word is Stopped since app-council A91, and
  // Stopped can also be a pipeline row's honest answer, so the guard reads the
  // header ingest chip itself rather than the whole page.
  // "Loading…" as well: the chip says that before its first reading now, and
  // a guard that only refused "Stopped" would pass on it at once.
  await expect(page.getByTestId("chrome-ingest-status")).not.toHaveText(/^(Stopped|Loading…)$/, {
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
  ]);

  // The fan-out, framed on its own: thirteen destinations across five
  // platforms and three rendition tiers, each card with its own tracks. This
  // used to stand in for the hero above, whose ingest card first read OFFLINE
  // over a live relay and then showed whichever programme reported last; both
  // are fixed (lib/dashboardFacts programmeSnapshot), and the site now shows
  // 01-dashboard.png itself. This frame stays for the per-destination claim.
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
  //
  // Matched on "Studio B", not on a full name, because two seeders feed this
  // spec and they name the second programme differently: seed_demo.go
  // (capture-media.sh) says "Studio B — panel show", demo-seed.fixture.json
  // (demo-seed.sh) says "Commentary Booth — Studio B". The full-name match made
  // this shot fail on every demo-seed run, so the site's capture set was always
  // one short. Visible-only, so the closed programme picker cannot satisfy it.
  // The lane heading is an <h3 data-lane-state> (Dashboard.tsx LaneHeading).
  // Matching the text "Studio B" alone passes on any visible element that
  // carries it (the programme picker, a header span), which is how this shot
  // once photographed the top of a one-programme dashboard.
  const laneHeadings = page.locator("h3[data-lane-state]");
  const laneHeading = laneHeadings.filter({ hasText: /Studio B/ }).first();
  await expect(
    laneHeading,
    "no lane heading for the second programme, so this is a single-programme " +
      "dashboard being photographed under a multi-programme name",
  ).toBeVisible({ timeout: 120_000 });
  expect(await laneHeadings.count(), "fewer than two lane headings").toBeGreaterThan(1);

  // Framed on the line between the two lanes: the end of the first programme's
  // cards, the second programme's heading and its first cards.
  await laneHeading.evaluate((el) => el.scrollIntoView({ block: "center", behavior: "instant" }));
  await settle(page, 700);
  // In the frame, and with the first lane's end above it: a heading at the very
  // top of the window has no lane boundary over it.
  await expect(laneHeading).toBeInViewport({ ratio: 1 });
  const box = (await laneHeading.boundingBox())!;
  expect(box.y, "the second lane's heading is at the top of the frame: no end of the first lane above it").toBeGreaterThan(250);
  await shoot(page, `${OUT}/19-lanes.png`, ["Waiting for a stream…", "Ingest offline"], {
    settleMs: 300,
  });
  // The heading must still be in the frame the shutter took (shoot() waits and
  // re-settles, and a re-layout could have moved it).
  await expect(laneHeading).toBeInViewport({ ratio: 1 });
});

test("routing — the thing nothing else does", async ({ page }) => {
  // The sports scene is photographed only once every destination has settled.
  if (SCENE === "sports") await waitSettled(page);
  await page.goto("/routing");
  await populated(page, EMPTY.routing);
  // Track rows are drawn from the PROBED layout, so a routing shot taken
  // before the probe lands is a mixer with no channels in it -- a page that
  // renders perfectly and demonstrates nothing.
  await expect(page.getByText("no signal")).toHaveCount(0, { timeout: 60_000 });
  await settle(page);
  // The sports scene has six tracks, and at 900 px the sixth row (Crowd, ticked
  // for every destination) falls below the frame. A taller window shows all six
  // rows, the ticks and the output after routing.
  const routingViewport = page.viewportSize()!;
  if (SCENE === "sports") {
    await page.setViewportSize({ width: routingViewport.width, height: 1100 });
    await settle(page, 600);
  }
  await snap(page, "02-routing.png", { fullPage: false });
  if (SCENE === "sports") await page.setViewportSize(routingViewport);

  // The compiled filtergraph, which is the claim this product makes about
  // being inspectable. Worth its own frame rather than being cropped out.
  // Since A27 it sits in the collapsed Advanced section, behind a button, so
  // both are opened first; guarded like the rest of this optional frame.
  await page.locator('[data-testid="routing-advanced"] summary').first().click().catch(() => {});
  await page.getByRole("button", { name: "Show FFmpeg graph" }).first().click().catch(() => {});
  const graph = page.locator("text=/pan=stereo/").first();
  const graphShown = await graph.isVisible().catch(() => false);
  // The manifest expects this file from the sports scene, so its absence there
  // is a failure and not a skipped frame.
  if (SCENE === "sports") expect(graphShown, "the FFmpeg graph (pan=stereo) is not on the page").toBe(true);
  if (graphShown) {
    await graph.scrollIntoViewIfNeeded();
    await settle(page, 400);
    await snap(page, "03-routing-filtergraph.png");
  }
});

/* The clip for the narrow column. Its own test so the sports scene, which has
 * no music track, does not take it: docs/media/shots.json gives it the full
 * scene. It opens the same way the routing test does. */
test("routing — the clip for narrow columns", async ({ page }) => {
  await page.goto("/routing");
  await populated(page, EMPTY.routing);
  await expect(page.getByText("no signal")).toHaveCount(0, { timeout: 60_000 });

  // THE HOME PAGE HERO, AS ITS OWN FRAME (UX Council S1).
  //
  // The hero used to crop 02-routing.png in CSS: the whole 1440px screen scaled
  // into a ~560px box, so the track names printed at about 5px and the crop
  // offsets were tuned to one capture's pixel layout and broke on the next.
  // This is the part the pitch is about, at a size it can be read: the track
  // rows of the selected destination, the music track visibly not sent, and the
  // music-rights switch that keeps it out.
  //
  // The selected destination is the first in the list, which the fixture makes
  // YouTube with the music bed excluded. Asserted rather than assumed: a hero
  // photographing a destination that DOES carry the music would argue against
  // the sentence beside it.
  //
  // A SKIP, NOT A FAILURE, when it is missing. capture-media.sh runs this same
  // spec against seed_demo.go's install, whose first destination carries the
  // music; failing here would stop that script before it compresses or copies
  // anything, over a frame it was never going to use. demo-seed.sh's fixture
  // is the one the hero is taken from.
  const excluded = await page
    .getByText("Music excluded", { exact: false })
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .then(() => true)
    .catch(() => false);
  if (!excluded) {
    test.info().annotations.push({
      type: "skipped-shot",
      description: "02-routing-hero.png: the selected destination carries the music, so the hero frame would contradict the home page",
    });
    return;
  }
  // At 1024px wide, not 1440. The hero column is ~560 CSS px, and at 1440 the
  // track card is 1000px of mostly meter bar, which the column shrinks until
  // the track names are unreadable again. At 1024 the card is ~600px, so the
  // hero shows it at close to its real size.
  //
  // AND TALL ENOUGH TO HOLD THE WHOLE CLIP. The music-rights card sits below
  // the four track rows, and a clip is cut from the viewport, not the page:
  // at 900px it ended on Track 4 under the sticky save bar, and the switch the
  // clip exists to show was never in it. 1800px holds the tab row through the
  // card's "Also exclude" line with room to spare.
  const fullViewport = page.viewportSize()!;
  await page.setViewportSize({ width: 1024, height: 1800 });
  await settle(page, 800);
  const simpleTab = page.getByText("Simple", { exact: true }).first();
  // Scrolled to the top and then back down past the app's sticky header,
  // which otherwise sits over the tab row the clip starts on.
  await simpleTab.evaluate((el) => {
    el.scrollIntoView({ block: "start", behavior: "instant" });
    let box: HTMLElement | null = el.parentElement;
    while (box && !(box.scrollHeight > box.clientHeight && getComputedStyle(box).overflowY !== "visible")) {
      box = box.parentElement;
    }
    (box ?? document.scrollingElement!).scrollBy(0, -88);
  });
  await settle(page, 800);
  const top = (await simpleTab.boundingBox())!;
  const tail = (await page.getByText("Also exclude", { exact: false }).first().boundingBox())!;
  // Asserted, not assumed: a clip whose bottom edge falls outside the viewport
  // is silently cut there, which is how the switch went missing before.
  expect(tail.y + tail.height + 18, "the music-rights card ends below the viewport").toBeLessThan(
    page.viewportSize()!.height,
  );
  const vw = page.viewportSize()!.width;
  const x = Math.max(0, top.x - 14);
  const y = Math.max(0, top.y - 10);
  // Tracks 3 and 4 can read "not measured, last peak at …" in this frame:
  // level frames name no programme, the demo's other two programmes carry two
  // tracks, and whichever engine spoke last decides what tracks 3 and 4 show
  // (found while re-aiming this, reported, not fixed here). A presence check
  // around the shutter cannot hold it -- it toggles faster than a screenshot
  // -- so the committed frame is one checked by eye to show all four meters.
  await snap(page, "02-routing-hero.png", {
    clip: { x, y, width: vw - 12 - x, height: tail.y + tail.height + 18 - y },
  });
  await page.setViewportSize(fullViewport);
});

test("meters — loudness measured after routing", async ({ page }) => {
  // The sports scene is photographed only once every destination has settled.
  if (SCENE === "sports") await waitSettled(page);
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
  // it at all -- and web/src/pages/features.astro quotes three of them in body
  // copy and alt text as the whole of its proof section. Waiting on the badge
  // rather than on another sleep, because the analyser's start-up is not a
  // fixed cost.
  await expect(
    page.getByText("NOT UPDATING"),
    "the loudness analyser never reported, so this shot has no per-destination " +
      "figures and the /features proof section quotes numbers that are not in it",
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
  // Five destinations' compliance rows, not two and a half: taller in the
  // sports scene so every "PASS" row is in the frame.
  const metersViewport = page.viewportSize()!;
  if (SCENE === "sports") {
    await page.setViewportSize({ width: metersViewport.width, height: 1300 });
    await settle(page, 600);
  }
  await snap(page, "04-meters.png");
  if (SCENE === "sports") await page.setViewportSize(metersViewport);
});

/** THE CAPTION OF 05-sources' `degraded` ALLOWANCE. The shot's manifest entry lets
 *  the word "degraded" through the failed-state guard because the failover card
 *  carries static help text with it ("Leave a degraded link"). This is the
 *  screen where a REAL degraded state would also appear, so the allowance is
 *  only honest if no source is in one: read from the API the page reads
 *  (/sources: listenerHealth; /source: degraded) and from the badges themselves,
 *  not from page text. */
async function assertNoSourceDegraded(page: import("@playwright/test").Page) {
  const states = await page.evaluate(async () => {
    const rows = (await (await fetch("/api/v1/sources", { credentials: "same-origin" })).json()) as {
      id: number;
      name: string;
      listenerHealth?: { state?: string };
    }[];
    const out: { name: string; listener: string; degraded: string }[] = [];
    for (const r of rows) {
      const info = (await (await fetch(`/api/v1/source?source=${r.id}`, { credentials: "same-origin" })).json()) as {
        degraded?: string;
      };
      out.push({ name: r.name, listener: r.listenerHealth?.state ?? "", degraded: info.degraded ?? "" });
    }
    return out;
  });
  expect(states.length, "no source rows to check").toBeGreaterThan(0);
  expect(
    states.filter((s) => s.listener === "degraded" || s.degraded !== ""),
    "a source is in a degraded state, so the `degraded` allowance on 05-sources is no longer only static help text",
  ).toEqual([]);
  // The badges the page draws for those states (sources.listenerDegraded,
  // sources.degraded in en.json).
  await expect(page.getByText("Partly bound", { exact: true })).toHaveCount(0);
  await expect(page.locator("output").filter({ hasText: "Link degraded" })).toHaveCount(0);
}

test("sources — one port, many programmes", async ({ page }) => {
  // The sports scene is photographed only once every destination has settled.
  if (SCENE === "sports") await waitSettled(page);
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
  // The sports scene is the opposite claim: one match feed, one source. Exactly
  // one is expected there, so a stray second source is a failure too.
  if (SCENE === "sports") {
    await expect.poll(() => perSource.count(), { timeout: 30_000 }).toBe(1);
  } else {
    await expect.poll(() => perSource.count(), { timeout: 30_000 }).toBeGreaterThan(1);
  }
  // RE-AIMED. Each programme's card now carries its caption track and caption
  // words above the ingest fields, so a card is about a thousand pixels tall
  // and the 900px frame at the top of the page held the first programme's
  // caption-word boxes and nothing else -- under a caption about three
  // programmes on one port. Taller, and framed from the first programme's
  // "Signal detected" and publish URL down through the second programme's
  // whole card to the third programme's header: three programmes, two of their
  // per-programme publish URLs, one port.
  const fullViewport = page.viewportSize()!;
  if (SCENE === "sports") {
    // One source: from the top of its card (name, DEFAULT, RUNNING) down through
    // the ingest, "Signal detected", the publish URL and the PUBLISHING line.
    await page.setViewportSize({ width: fullViewport.width, height: 1500 });
    await expect(page.getByText("Signal detected").first()).toBeVisible({ timeout: 30_000 });
    await page.locator("main").evaluate((m) => m.scrollTo(0, 0));
    await settle(page);
  } else {
    await page.setViewportSize({ width: fullViewport.width, height: 1500 });
    const names = page.locator("[data-slot=card-title]");
    await expect.poll(() => names.count(), { timeout: 30_000 }).toBeGreaterThan(2);
    await topOfFrame(page, page.getByText("Signal detected").first());
    // Clear of the sticky header, which would otherwise sit on the line framed.
    await page.locator("main").evaluate((m) => m.scrollBy(0, -64));
    await settle(page);
    // The third programme's name has to be in the frame, or this is two.
    const third = (await names.nth(2).boundingBox())!;
    expect(third.y + third.height, "the third programme is below the frame").toBeLessThan(1500);
  }
  // Masked last, right before the shutter: the resize and the scroll above
  // re-render the cards, and a re-render can put a token back.
  await maskPublishTokens(page);
  await assertNoSourceDegraded(page);
  await snap(page, "05-sources.png");
  await assertNoTokenSurvived(page);
  await page.setViewportSize(fullViewport);
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

  // The API must still hand out credentials, or this function is masking an
  // empty set and every check below it is vacuous.
  expect(masked.tokens, "no publish tokens came back from the API").toBeGreaterThan(0);

  /* `hits > 0` USED TO BE ASSERTED HERE, and it was a proxy for the property
   * that matters rather than the property itself.
   *
   * The reasoning was sound when written: a mask that matched nothing might
   * mean the field had moved, leaving the credential on screen. But the
   * product has since started masking its own credentials -- SecretCode
   * renders `"•".repeat(16)` and puts the real value in the DOM only when the
   * operator reveals it -- so on this page there is now legitimately nothing
   * for this sweep to hit. The assertion began failing on a page that had
   * become SAFER, and it blocked 05-sources.png from ever being refreshed.
   *
   * Deleting it outright would restore the original hazard, so it is not
   * deleted so much as relocated: assertNoTokenSurvived, which runs after the
   * shutter, checks the real question directly -- is any live token present in
   * the page's text or in any field value. A field that moves and leaks fails
   * THAT, whether or not this sweep found anything. The mask stays because a
   * future page may well print a token in plain text again; it just no longer
   * has to succeed for the shot to be taken.
   *
   * `hits` is returned for the log rather than thrown away, because "the mask
   * hit nothing" is worth being able to see when a shot looks wrong. */
  if (masked.hits === 0) {
    // eslint-disable-next-line no-console
    console.log(
      "capture: no plaintext token found to mask -- the page masks its own " +
        "credentials. assertNoTokenSurvived below is what proves the shot is clean.",
    );
  }
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
  await snap(page, "06-renditions.png");
});

test("monitoring — every process, with its own FFmpeg output", async ({ page }) => {
  await page.goto("/monitoring");
  await settle(page, 2500);
  // THE WHOLE PAGE NOW, process list and log included. This frame used to stop
  // above the process list, because every child that decodes video from the
  // relay joins it mid-GOP and, until the next keyframe, FFmpeg's h264 decoder
  // complains once a frame -- and the supervisor counted that burst as errors,
  // so a healthy install showed "189 ERRORS" on whichever process started last.
  // The supervisor now logs a video decoder's lines as info until the process's
  // first output frame (internal/supervisor/warmup.go), so a healthy install
  // shows no errors, and the page can be photographed as it is.
  //
  // Framed on the process list, with the log filtered to one process: the
  // claim is "every process, with its own FFmpeg output", and the filtered log
  // is the "its own".
  const processes = page.getByText("Processes", { exact: true }).first();
  await topOfFrame(page, processes);

  // The log opens on Errors, and on a healthy install that view is empty --
  // which is the point, and is also a picture of nothing. All shows the lines.
  await page.getByRole("button", { name: "All", exact: true }).click();

  // THE FFMPEG LOG IS NOT IN THIS FRAME, and the reason is measured, not a
  // preference. A log shows "ordinary progress" only for a process that prints
  // some. At the server's default level a destination's FFmpeg, a loudness
  // monitor and the playout source print NOTHING (the log reads "No lines match
  // this view."), and the only processes with lines are the video decoders
  // (preview, meters, the renditions), whose join-time warm-up lines
  // ("non-existing PPS 0 referenced", "no frame!", "Last message repeated 17
  // times") read as a failing decoder under an "ERRORS 0" chip. Raising FFmpeg's
  // level (POLYEMESIS_FFMPEG_LOGLEVEL=info) gives the destinations lines but
  // makes the supervisor count one error per process, which this shot must not
  // show either. So the frame is the process list, with the log below it left
  // out, and this asserts the log is not in what is photographed.
  const logPane = page.locator('[class~="h-[22rem]"]');

  // NO FALSE ERRORS: no per-process error chip, and the log's own count at 0.
  // Asserted rather than hoped: a frame showing "N errors" on a healthy stream
  // is the product looking broken, which this harness refuses to photograph.
  const falseErrors = page.getByText(/^\d+ errors?$/);
  await expect(falseErrors).toHaveCount(0, { timeout: 30_000 });
  // The process list is a table now (A72): its count is a bare number in the
  // Errors column, which the "N errors" text match above cannot see.
  const processTable = page
    .getByRole("table")
    .filter({ has: page.getByRole("columnheader", { name: "Errors", exact: true }) });
  await expect
    .poll(async () =>
      (await processTable.locator("tbody tr > td:last-child").allInnerTexts())
        .map((t) => t.trim())
        .filter((t) => /^\d+$/.test(t) && t !== "0"),
    )
    .toEqual([]);
  await expect(page.getByText("Errors 0", { exact: true })).toBeVisible();
  await expect(page.getByText(/^No errors in the last \d+ s$/)).toBeVisible();
  // And the filtered log has lines in it: an empty log would be a picture of
  // nothing, whatever its count says.
  await expect(page.getByText("No lines match this view.", { exact: true })).toHaveCount(0);
  // Every running process has an uptime: the meters used to read --:--:--
  // for their whole life, because their stdout is level data and the
  // supervisor never heard that media was flowing.
  await expect(page.getByText("--:--:--", { exact: true })).toHaveCount(0);
  await expect(page.getByText("No lines match this view.", { exact: true })).toHaveCount(0);
  // From the Processes heading to the bottom of its table, a little wider than
  // the table: the log card below it is outside the clip.
  const head = (await processes.boundingBox())!;
  const table = (await processTable.boundingBox())!;
  const logBox = (await logPane.boundingBox())!;
  const bottom = table.y + table.height + 16;
  expect(bottom, "the Processes table runs into the log pane").toBeLessThanOrEqual(logBox.y);
  await snap(page, "07-monitoring.png", {
    clip: { x: Math.max(0, table.x - 20), y: Math.max(0, head.y - 28), width: table.width + 40, height: bottom - Math.max(0, head.y - 28) },
  });
  await expect(falseErrors).toHaveCount(0);
  // The decoder lines are in the log pane only; none may be in the card.
  expect(await processTable.innerText(), "a decoder warm-up line is inside the photographed table").not.toMatch(/no frame|non-existing/i);
});

/* ---------------------------------------------------------------- part two
 *
 * The seven shots above cover ingest and routing. These cover the rest of the
 * product, because a README that shows only the differentiator implies there is
 * nothing else — and the post-production, chat and automation surfaces are most
 * of what an operator touches after the first week.
 */

test("routing mix matrix — channel-level control", async ({ page }) => {
  // The sports scene is photographed only once every destination has settled.
  if (SCENE === "sports") await waitSettled(page);
  await page.goto("/routing");
  await populated(page, EMPTY.routing);
  await settle(page);
  // SPORTS: THE LAST SHOT OF THE SCENE, BY DESIGN. The shot list wants the
  // six-track matrix for English with tracks 1 and 6 lit. English runs in simple
  // mode, and clicking the Mix matrix tab on it would only patch an UNSAVED draft
  // (and saving restarts a live destination). So this puts English into matrix
  // mode through the console's own API -- the same PUT the editor's Save sends --
  // waits for it to be live again with measured loudness, and shoots. The restart
  // is harmless only because nothing is photographed after it: this test is the
  // last of the sports scene in this file, and the rig is torn down next.
  if (SCENE === "sports") await putEnglishInMatrixMode(page);
  // FULL: SELECT A DESTINATION THAT IS ALREADY IN MATRIX MODE FIRST.
  //
  // Clicking the tab on whichever destination happens to be selected -- the
  // first in the list, which is in simple mode -- SWITCHES ITS MODE. What that
  // photographs is a grid of zeros with an UNSAVED badge in the corner.
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
  // A missing one is a failure, never a quiet skip, and never a picture of an
  // empty grid that looks like the feature not working.
  expect(matrixName, "no destination on this install uses the mix matrix").not.toBeNull();
  if (SCENE === "sports") {
    await page.reload();
    await populated(page, EMPTY.routing);
    await settle(page);
  }
  await page.getByText(matrixName!.slice(0, 12), { exact: false }).first().click();
  await settle(page, 500);

  // The matrix subsumes simple mode and is the more striking image: a grid of
  // per-channel gains rather than a column of checkboxes.
  const tab = page.getByText("Mix matrix", { exact: true }).first();
  await expect(tab).toBeVisible({ timeout: 15_000 });
  await tab.click();
  await settle(page, 800);
  if (SCENE === "sports") {
    // Six tracks, none of them UNSAVED, and the loudness measured again.
    await expect(page.getByText(/^unsaved$/i)).toHaveCount(0);
    // Tracks 1 and 6 lit, every other row dark: read from the grid's own cells.
    await expect(page.getByRole("spinbutton", { name: "Track 1 channel 1 to left" })).toHaveValue("1");
    await expect(page.getByRole("spinbutton", { name: "Track 6 channel 2 to right" })).toHaveValue("1");
    await expect(page.getByRole("spinbutton", { name: "Track 2 channel 1 to left" })).toHaveValue("0");
    const vp = page.viewportSize()!;
    // Wide enough for all six tracks' L and R columns (the grid scrolls sideways
    // at 1440), and tall enough for the grid and the save bar below it.
    await page.setViewportSize({ width: 1800, height: 1020 });
    await settle(page, 800);
    await expect(page.getByRole("spinbutton", { name: "Track 6 channel 2 to right" })).toBeInViewport({ ratio: 1 });
    await snap(page, "08-mix-matrix.png");
    await page.setViewportSize(vp);
    return;
  }
  await snap(page, "08-mix-matrix.png");
});

/** Puts the first sports destination (YouTube · English) in matrix mode with
 *  tracks 1 (English) and 6 (Crowd) on, through PUT /destinations/{id}, then
 *  waits until every destination is live again with measured loudness. */
async function putEnglishInMatrixMode(page: import("@playwright/test").Page) {
  const csrf = (await page.context().cookies()).find((c) => c.name.toLowerCase().includes("csrf"))?.value ?? "";
  const list = await page.request.get("/api/v1/destinations");
  expect(list.ok()).toBe(true);
  const rows = (await list.json()) as { destination: Record<string, unknown> & { id: number; name: string } }[];
  const english = rows.map((r) => r.destination).find((d) => d.name.endsWith("English"));
  expect(english, "no destination named ... English on this install").toBeTruthy();
  const profile = { ...(english!.profile as Record<string, unknown>) };
  const tracks = profile.tracks as { track: number; enabled: boolean; gain: number }[];
  expect(tracks.length, "the sports source has six tracks").toBeGreaterThanOrEqual(6);
  profile.mode = "matrix";
  profile.tracks = tracks.map((t) => ({ ...t, enabled: t.track === 0 || t.track === 5 }));
  profile.matrix = [
    { track: 0, channel: 0, out: 0, gain: 1 },
    { track: 0, channel: 0, out: 1, gain: 1 },
    { track: 5, channel: 0, out: 0, gain: 1 },
    { track: 5, channel: 1, out: 1, gain: 1 },
  ];
  const res = await page.request.put(`/api/v1/destinations/${english!.id}`, {
    data: { ...english, profile },
    headers: { "X-CSRF-Token": csrf },
  });
  expect(res.ok(), `PUT of the English destination answered ${res.status()}`).toBe(true);
  // The save restarts it; it is shot only once it has run long enough again.
  await page.waitForTimeout(3000);
  await waitSettled(page);
}

/* THE FIRST STUBBED SHOT, AND IT IS DECLARED RATHER THAN QUIET.
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
 * Recorded in docs/media/README.md as an exception to "generated, not
 * hand-made", because an undeclared exception is how a policy stops meaning
 * anything.
 */
const CHAT_PLATFORMS = ["youtube", "twitch", "facebook", "kick"] as const;

// The optional fourth field marks a message polyemesis sent itself (a poll
// announcement), which the panel draws with the megaphone and a tinted row.
// The long Kick line exists to wrap: the shot shows the hanging indent.
const DEMO_CHAT: readonly (readonly [string, string, string, boolean?])[] = [
  ["twitch", "hexcode", "wait, one ingest and it fans out to all four?"],
  ["youtube", "mira_dev", "the per-destination audio thing is the bit I want"],
  ["kick", "nightowl_", "so the VOD track drops the music bed automatically, and the live mix keeps it? that is exactly what I wanted for my channel, no more muting the whole stream"],
  ["facebook", "Dana Whitfield", "audio is noticeably cleaner this week"],
  ["youtube", "sam_tk", "how many destinations can it drive at once?"],
  ["twitch", "brb_afk", "5.5 Mbps and zero dropped frames, that's the whole point"],
  ["kick", "vexcarter", "does moderation reach back to the platform or just hide it here"],
  ["facebook", "Ana Ruiz", "one timeline for four chats is genuinely the feature"],
  ["twitch", "polyemesis", "Poll closed: which mix for the VOD? Voice only 62%, Full mix 38%", true],
  ["youtube", "mira_dev", "voice only, obviously"],
];

test("chat — four platforms in one hub", async ({ page }) => {
  // WHICH PLATFORMS THE SEND BOX MAY NAME comes from the server's capability
  // matrix, the same one the Platforms page and docs/PLATFORMS.md print, rather
  // than from a literal here. The fixture used to say `canSend: true` for all
  // four, and the shot read "Send to YouTube, Twitch, Facebook, Kick" while
  // PLATFORMS.md says Facebook chat send is "Not possible". The hub decides
  // canSend from that matrix (chat.canPost); the stand-in now asks it too.
  const capsRes = await page.request.get("/api/v1/platforms/capabilities");
  expect(capsRes.ok()).toBe(true);
  const caps = (await capsRes.json()) as {
    platforms: { platform?: string; capabilities: Record<string, string> }[];
  };
  const canSend = (platform: string) =>
    caps.platforms.find((p) => p.platform === platform)?.capabilities.chatSend !== "no";
  expect(canSend("facebook"), "the matrix still marks Facebook chat send as not possible").toBe(false);

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
            canSend: canSend(platform),
            received: DEMO_CHAT.filter((m) => m[0] === platform).length,
            sent: i === 0 ? 1 : 0,
            restarts: 0,
          })),
          // Newest last, seconds apart, so the timeline reads as a conversation
          // rather than as a burst that all arrived at once.
          messages: DEMO_CHAT.map(([platform, name, text, echo], i) => ({
            id: `demo-${i}`,
            platform,
            account: platform === "facebook" ? "Polyemesis" : "polyemesis",
            channel: platform === "facebook" ? "Polyemesis Live" : "polyemesis",
            author: { id: `u-${name}`, name },
            text,
            at: new Date(Date.now() - (DEMO_CHAT.length - i) * 17_000).toISOString(),
            ...(echo ? { echo: true } : {}),
          })),
          limits: CHAT_PLATFORMS.filter(canSend).map((platform) => ({
            platform,
            maxChars: platform === "youtube" ? 200 : 500,
          })),
        },
      }),
  );

  // The composer's "Sending to" chips come from GET /chat/live: a send with no
  // target goes only to platforms that are live. YouTube and Twitch are on air
  // here and Kick is connected but not, so the shot shows both chip states.
  await page.route(
    (url) => url.pathname === "/api/v1/chat/live",
    (route) =>
      route.fulfill({
        json: {
          anyLive: true,
          targets: CHAT_PLATFORMS.filter(canSend).map((platform) => ({
            platform,
            account: "polyemesis",
            canSend: true,
            live: platform !== "kick",
          })),
        },
      }),
  );

  // THE TOAST'S CAUSE, REMOVED AT THE SOURCE. With a connected YouTube account in
  // the stubbed overview above, the moderator card (ChatModerators.tsx) asks
  // GET /api/v1/chat/moderators, and this install has no chat account, so the
  // server answers with an error that the card raises as a red toast. Stubbed
  // here the way the overview is: an empty moderator list.
  await page.route(
    (url) => url.pathname === "/api/v1/chat/moderators",
    (route) => route.fulfill({ json: { moderators: [] } }),
  );

  // A shorter viewport, clipped to the timeline card and its composer. At the
  // full 1440x900 the ten messages fill a third of the panel and the rest is
  // empty space. The site shows this shot at about 600px wide, so the clip
  // keeps the message text legible.
  await page.setViewportSize({ width: 1440, height: 720 });
  await page.goto("/chat");
  await settle(page, 1500);
  // NO TOAST AT ALL, and none is ever closed unread: a toast that appears is a
  // failure of this capture whose text goes into the message.
  await page.waitForTimeout(3000);
  expect(
    await page.locator("[data-sonner-toast]").allInnerTexts(),
    "the chat page raised a toast; it is not to be closed unread, find its cause",
  ).toEqual([]);
  await snap(page, "09-chat.png", { clip: { x: 180, y: 150, width: 950, height: 560 } });
});

test("playout — serving a player from polyemesis itself", async ({ page }) => {
  await page.goto("/playout");
  await settle(page, 1500);
  await snap(page, "10-playout.png");
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
  await snap(page, "11-library.png");
});

test("jobs — the post-production queue and its governor", async ({ page }) => {
  await page.goto("/jobs");
  await settle(page, 1500);
  await snap(page, "12-jobs.png");
});

/** The schedules and alert rules the fixture seeds (demo-seed.fixture.json,
 *  "automation"). Asserted by name, so an empty panel or a stale install fails
 *  this shot rather than photographing "No alert rules yet". */
const DEMO_ALERT_RULES = [
  "Ops — #broadcast-alerts",
  "On-call pager — critical only",
  "Audio desk — clipping and loudness",
  "Security log — sign-ins and tokens",
  "Recording volume — disk watch",
];
const DEMO_SCHEDULES = [
  "Weeknight show — on air 19:00",
  "Archive — stop recording 21:30",
  "Overnight filler — playlist from 02:00",
  "Saturday rehearsal — on air 11:00",
];

test("automation — schedules and alert rules", async ({ page }) => {
  await page.goto("/automation");
  await populated(page, "No alert rules yet");
  // The schedules live on their own tab, so one frame holds one of the two: the
  // alert rules are shot, and the schedules are asserted here before it, so a
  // seed that lost them fails the shot.
  await page.getByRole("tab", { name: "Schedules" }).click();
  for (const name of DEMO_SCHEDULES) {
    await expect(page.getByText(name).first(), `schedule ${name} is not on the Schedules tab`).toBeVisible({ timeout: 30_000 });
  }
  await page.getByRole("tab", { name: "Alerts" }).click();
  for (const name of DEMO_ALERT_RULES) {
    await expect(page.getByText(name, { exact: true }).first(), `alert rule ${name} is not on the Alerts tab`).toBeVisible({ timeout: 30_000 });
  }
  await settle(page, 1500);
  await snap(page, "13-automation.png");
});

test("clips — the editor", async ({ page }) => {
  await page.goto("/clips");
  // THE BUFFER ARMED AND A CLIP CAPTURED FROM IT: neither the "rolling buffer is
  // off" empty state nor "No clips yet." is the picture.
  await expect(page.getByText("The rolling buffer is off")).toHaveCount(0, { timeout: 30_000 });
  await expect(page.getByText("No clips yet.")).toHaveCount(0, { timeout: 30_000 });
  await expect(page.getByText(/clip-\d{8}-\d{6}\.ts/).first(), "no captured clip is listed").toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("Holding", { exact: false }).first()).toBeVisible();
  await settle(page, 1500);
  await snap(page, "14-clips.png");
});

test("recordings — segments and retention", async ({ page }) => {
  await page.goto("/recordings");
  await populated(page, EMPTY.recordings);
  await settle(page, 1500);
  await snap(page, "15-recordings.png");
});

test("automod — the rules, and the matrix that grants them", async ({ page }) => {
  // The pipeline tab, because that is where the cards are mounted: the pattern
  // rules, the model checker, then the grant matrix ("Automatic moderation").
  await page.goto("/settings?tab=pipeline");

  const toggle = page.getByRole("switch", { name: /automatic moderation/i }).first();
  await expect.poll(() => toggle.count(), { timeout: 30_000 }).toBeGreaterThan(0);
  // Seeded on, so a screenshot of it off would be a screenshot of the feature
  // switched off -- worth asserting rather than hoping.
  await expect(toggle).toBeChecked();

  // BOTH ARE THE SHOT, and either one missing fails it. The rules the fixture
  // seeds (settings.automod.rules), by name, in their inputs; the matrix by a
  // cell, which is inside a platform row and needs the Twitch row opened.
  const rules = page.locator("[data-slot=card-title]", { hasText: "Pattern rules" }).first();
  await expect(rules, "no Pattern rules card").toBeVisible({ timeout: 30_000 });
  for (const name of ["Link spam", "Shouting", "Giveaway scam"]) {
    await expect(
      page.locator(`input[value="${name}"]`).first(),
      `the seeded rule ${name} is not in the Pattern rules card`,
    ).toBeVisible();
  }
  // A pattern the page's own check cannot compile prints a warning under it.
  await expect(page.getByText("This pattern does not compile")).toHaveCount(0);
  const matrix = page.locator("[data-slot=card-title]", { hasText: "Automatic moderation" }).first();
  await expect(matrix, "no grant matrix card").toBeVisible();
  await page.getByRole("button", { name: /^twitch \d+ automatic actions?$/i }).click();
  // automod.cellLabel: "{checker} may {action} on {platform}".
  const cell = page.getByRole("switch", { name: / may .* on twitch$/i }).first();
  await expect(cell, "no grant matrix cell is visible under Twitch").toBeVisible({ timeout: 15_000 });

  // A frame tall enough for the rules above and the matrix below, with the
  // viewport scrolled so that the first rule card is at the top of it (clear of
  // the app header and the tab's sticky save bar), and cut at the last platform
  // row of the matrix.
  const vp = page.viewportSize()!;
  const main = page.locator("main");
  await page.setViewportSize({ width: vp.width, height: 3400 });
  await settle(page, 600);
  await topOfFrame(page, rules);
  await main.evaluate((m) => m.scrollBy(0, -110));
  await settle(page);
  const last = (await page.getByRole("button", { name: /^rumble /i }).boundingBox())!;
  await page.setViewportSize({ width: vp.width, height: Math.ceil(last.y + last.height) + 40 });
  await settle(page, 600);
  await expect(rules).toBeInViewport({ ratio: 1 });
  await expect(matrix).toBeInViewport({ ratio: 1 });
  await expect(cell).toBeInViewport({ ratio: 1 });
  await snap(page, "20-automod.png");
  await page.setViewportSize(vp);
});

test("settings — listeners, and the one-port design in the UI", async ({ page }) => {
  await page.goto("/settings");
  await settle(page, 1500);
  await snap(page, "16-settings.png");
  // THE WHOLE PAGE, not a second look at the same 900 px. <main> is the
  // scroller, so Playwright's fullPage cannot see past the window; the window is
  // made as tall as the page instead, and asserted to hold all of it. The page
  // is as long as it is: Settings shows one section at a time now, so this is
  // that section in full.
  const vp = page.viewportSize()!;
  const main = page.locator("main");
  const whole = await main.evaluate((m) => m.scrollHeight + m.getBoundingClientRect().top);
  await page.setViewportSize({ width: vp.width, height: Math.max(vp.height, Math.ceil(whole) + 16) });
  await settle(page, 800);
  const fits = await main.evaluate((m) => m.scrollHeight <= m.clientHeight + 1);
  expect(fits, "the page is still taller than the window, so this is not the whole page").toBe(true);
  await snap(page, "17-settings-full.png");
  await page.setViewportSize(vp);
});

test("platforms — sign-in and credentials, honestly not connected", async ({ page }) => {
  // Settings → Platform credentials, where each platform's account is attached.
  // The seeded install has NO account connected and never will: that needs live
  // OAuth credentials for a real channel. So this photographs the genuine
  // not-connected state, which is what a new install shows, and nothing is
  // typed into any field.
  //
  // REDESIGNED (settings-council R1/R2): the tab is now an "Optional" banner
  // with the developer-app explanation one click down, a Connected channels
  // card (empty here), and an "Add a platform" list of one row per platform
  // with a status pill and a Set up button. The old frame anchored on a "Why
  // you need your own developer app" card that no longer exists. This opens
  // YouTube's row, so the shot is the one-time setup itself: the empty Client
  // ID and Client secret fields under the platform list.
  await page.goto("/settings?tab=platforms");
  const connected = page.getByTestId("platforms-connected");
  await expect(connected).toBeVisible({ timeout: 30_000 });
  await expect(connected.getByText("No channel connected yet")).toBeVisible();
  const setUp = page.getByRole("button", { name: /^Set up: YouTube/ });
  await expect(setUp).toBeVisible();
  await setUp.click();
  await expect(page.locator("#platform-panel-youtube").getByText(/client id/i).first()).toBeVisible();
  await topOfFrame(page, page.getByText("Optional.", { exact: true }));
  // <main> is the scroller, not the window: clear the banner of the header.
  await page.locator("main").evaluate((m) => m.scrollBy(0, -24));
  await settle(page, 1500);
  await snap(page, "21-platforms.png");
});

/* THE SECOND STUBBED SHOT, DECLARED FOR THE SAME REASON AS CHAT.
 *
 * 21-platforms.png is the one-time setup a new install starts in, and it is
 * honest -- but it was the only picture of platform sign-in, so the site showed
 * empty Client ID fields and a greyed Save button under a section promising
 * "stop pasting stream keys". That is the end state nobody sees until they have
 * a real channel to connect (UX Council S5).
 *
 * So this serves the end state to the REAL Settings page: a registered app and
 * one connected account per platform. Every name is obviously a demo name, the
 * client id says "demo" and "not-real" in plain words, and nothing resembles a
 * credential. What is genuine is the page: the connected-account rows, the
 * reconnect logic, the viewer-count readout. What is fabricated is only whose
 * channel it is. Listed in docs/media/README.md beside 09-chat.png. */
const DEMO_ACCOUNTS = [
  { platform: "youtube", accountName: "Demo Studio (YouTube)", accountRef: "channel/demo-studio" },
  { platform: "twitch", accountName: "demo_studio", accountRef: "user/demo_studio" },
  { platform: "facebook", accountName: "Demo Studio Page", accountRef: "page/demo-studio" },
] as const;

test("platforms — connected, the end state of the one-time setup", async ({ page }) => {
  const now = new Date();
  const later = new Date(now.getTime() + 30 * 24 * 3600 * 1000).toISOString();
  await page.route(
    (url) => url.pathname === "/api/v1/platforms/credentials",
    (route) =>
      route.fulfill({
        json: DEMO_ACCOUNTS.map((a) => ({
          platform: a.platform,
          clientId: `demo-${a.platform}-client-not-real`,
          hasSecret: true,
          updatedAt: now.toISOString(),
        })),
      }),
  );
  await page.route(
    (url) => url.pathname === "/api/v1/platforms/accounts",
    (route) =>
      route.fulfill({
        json: DEMO_ACCOUNTS.map((a, i) => ({
          id: i + 1,
          platform: a.platform,
          accountName: a.accountName,
          accountRef: a.accountRef,
          expiresAt: later,
          scopes: "",
          scopeVer: 0,
          createdAt: now.toISOString(),
          updatedAt: now.toISOString(),
          reconnect: { needed: false },
        })),
      }),
  );
  // Offline, because it is: the seeded RTMP destinations point at .invalid
  // hosts and are disabled, so no platform is carrying this demo. A viewer
  // count here would be a number nobody measured.
  await page.route(
    (url) => /^\/api\/v1\/platforms\/accounts\/\d+\/stats$/.test(url.pathname),
    (route) => route.fulfill({ json: { supported: true, stats: { live: false } } }),
  );

  await page.goto("/settings?tab=platforms");
  // Since the redesign the connected accounts are rows in the Connected
  // channels card, each with a Connected pill, rather than a form per
  // platform. Asserted per row: a card that rendered its empty state would
  // otherwise photograph "No channel connected yet" under a "connected" name.
  const connected = page.getByTestId("platforms-connected");
  await expect(connected).toBeVisible({ timeout: 30_000 });
  for (const a of DEMO_ACCOUNTS) {
    await expect(
      connected.locator(`[data-platform-row="${a.platform}"]`).getByText(a.accountName),
      "the stubbed accounts did not render, so this would photograph the empty state under a 'connected' name",
    ).toBeVisible({ timeout: 20_000 });
  }
  // The LAST row opened, with its app credentials expanded: the other rows keep
  // their Connected pills above it, and the registered app's client id (an
  // input's value, which is why it is checked as one) and the viewer readout
  // are on the page too.
  const panel = page.locator("#platform-panel-facebook");
  await connected.locator('[data-platform-row="facebook"]').getByRole("button", { name: /Manage/ }).click();
  await panel.getByText(/App credentials/).first().click();
  await expect(
    panel.locator("input").first(),
    "the opened row does not show the demo client id",
  ).toHaveValue("demo-facebook-client-not-real", { timeout: 20_000 });
  await topOfFrame(page, page.getByText("Optional.", { exact: true }));
  await page.locator("main").evaluate((m) => m.scrollBy(0, -24));
  await settle(page, 1500);
  await snap(page, "22-platforms-connected.png");
});
