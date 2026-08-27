import { expect, test } from "@playwright/test";

import { EMPTY, OUT, populated, shoot, topOfFrame } from "./captureKit";

/* THE DASHBOARD AT ONE, TWO AND THREE PROGRAMMES.
 *
 * Dashboard.tsx is two different pages either side of a single threshold, and
 * the threshold is TWO, not three:
 *
 *   - one programme  -> destinationLayout returns grouped:false, laneLayout
 *                       returns laned:false, previewLayout returns grid:false.
 *                       One full-width player, a flat destination grid, no
 *                       programme badge on the ingest card.
 *   - two or more    -> lanes. Each programme becomes one box holding its own
 *                       preview pane AND the destinations carrying it, the
 *                       top-level preview grid is suppressed entirely, and the
 *                       ingest card gains a programme badge.
 *
 * Three is not a third shape. It is two with another lane, and photographing
 * it is how that claim stops being something a reader has to take on trust.
 *
 * ONE INSTALL, THREE SHOTS, TAKEN IN ORDER. scripts/capture-lanes.sh seeds one
 * programme, shoots, adds the second, shoots, adds the third, shoots -- rather
 * than standing up three installs or seeding three and deleting backwards.
 * Both alternatives were wrong in ways that would show:
 *
 *   - Three installs differ in every timestamp, port and generated id on the
 *     page, so the images differ everywhere EXCEPT the thing being compared.
 *   - Deleting backwards leaves the removed programme's destinations behind as
 *     orphans, which Dashboard.tsx draws in a flagged group by design. The
 *     one-programme shot would then carry a paragraph about destinations whose
 *     programme is missing: a fault state, not the shape being demonstrated.
 *
 * WHICH SHOT THIS IS comes from the environment, because the shell has to add
 * a programme between runs and Playwright cannot pause for that.
 */

/** How many programmes the install is expected to be carrying RIGHT NOW. */
const N = Number(process.env.LANES_N ?? "0");

/** The programme names seed_demo.go creates, in the order it creates them. */
const NAMES = ["Studio A", "Studio B — panel show", "Studio C — outside broadcast"];

/** The player's two placeholder texts. Both, because the player says "Ingest
 *  offline" when nothing is on air and "Waiting for a stream…" only while it
 *  buffers -- so forbidding the second alone passes on a tile showing the
 *  first, which is the more damning of the two. */
const PLACEHOLDERS = ["Waiting for a stream…", "Ingest offline"];

test(`dashboard at ${N} programme(s)`, async ({ page }) => {
  test.skip(N < 1 || N > 3, "LANES_N must be 1, 2 or 3");

  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();

  // THE COUNT IS THE SUBJECT, so it is asserted from both directions.
  //
  // A PROGRAMME'S NAME IS ONLY ON THIS PAGE WHEN LANES ARE. With one
  // programme destinationLayout returns grouped:false and no heading is
  // rendered at all, so "Studio A" appears nowhere on the single-programme
  // dashboard -- its absence is part of what that shot exists to show, and
  // asserting its presence would fail the very case being photographed.
  //
  // So index 0 is checked only from N >= 2, where the lane heading puts it on
  // screen. Everything after it is checked from both directions.
  for (let i = 0; i < NAMES.length; i++) {
    const name = NAMES[i];
    if (i === 0 && N < 2) continue;
    if (i < N) {
      await expect(
        page.getByText(name, { exact: false }).first(),
        `no sign of ${name}, so this install is carrying fewer than the ${N} ` +
          `programmes this shot is named for`,
      ).toBeVisible({ timeout: 120_000 });
    } else {
      // BOTH DIRECTIONS, and this is the half that matters. A run that failed
      // to add the third programme would still find the first two, pass, and
      // file a two-lane dashboard under a three-lane name -- which is exactly
      // the mistake 19-lanes.png was created to correct, made again one
      // programme further along.
      await expect(
        page.getByText(name, { exact: false }),
        `${name} is on screen, so this install is carrying more than the ${N} ` +
          `programmes this shot is named for`,
      ).toHaveCount(0, { timeout: 10_000 });
    }
  }

  // Destinations have to have arrived, or the lanes are empty boxes and the
  // shot argues that a second programme costs you a working dashboard.
  await populated(page, EMPTY.destinations);

  // Named by the caller, and refused rather than defaulted. A missing SHOT
  // would otherwise write `${OUT}/undefined`, which is a file nobody looks for
  // and a run that reports success.
  const shot = process.env.SHOT;
  if (!shot) throw new Error("SHOT is unset; nothing would name the screenshot");

  // FRAMED ON THE DESTINATION AREA, which is where the whole difference lives.
  //
  // Not fullPage, and not because a tall image would be unwieldy: the app
  // scrolls in an inner container, so Playwright's fullPage has no effect here
  // at all -- it returns the viewport and reports success. The first run of
  // this script produced three images cropped above the destination area,
  // which is to say three pictures of the half of the page that does not
  // change.
  //
  // topOfFrame puts the heading at the top of the shot rather than merely
  // on screen, so the three images start at the same element and can be read
  // against each other. 19-lanes.png frames itself the same way.
  await topOfFrame(page, page.getByRole("heading", { name: /^Destinations/ }));

  await shoot(page, `${OUT}/${shot}`, PLACEHOLDERS, { settleMs: 1200 });
});
