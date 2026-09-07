import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

/* IN src/lib, NOT BESIDE THE PAGE IT CHECKS, and the build is what said so.
 *
 * Astro treats EVERY file under src/pages as a route. A `.test.ts` there is
 * picked up as a page and executed during prerender, where its readFileSync of
 * a sibling .astro file throws and takes the whole build down. The first
 * version of this test sat next to features.astro and did exactly that -- and
 * vitest.config.ts had to be widened to see it there, which would have made the
 * mistake easy to repeat. Both are reverted: the allow-list stays narrow and
 * the test lives where tests live. */

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, "..", "pages", "features.astro"), "utf8");

/** Every `shot:` on the features page, in source order, with its section id. */
function sections(): { id: string; shot: string; alt: string }[] {
  // The page is a literal array of objects; read it as text rather than
  // importing it, because importing an .astro module from vitest drags in the
  // whole Astro pipeline to answer a question about a string.
  const out: { id: string; shot: string; alt: string }[] = [];
  const re = /id:\s*"([^"]+)"[\s\S]*?shot:\s*"([^"]+)"[\s\S]*?alt:\s*"([^"]+)"/g;
  for (const m of page.matchAll(re)) out.push({ id: m[1], shot: m[2], alt: m[3] });
  return out;
}

describe("the features page's screenshots", () => {
  it("finds the sections it is meant to be checking", () => {
    // A regex that stops matching would leave every assertion below passing
    // over an empty list, which is the same silence they exist to break.
    expect(sections().length).toBeGreaterThanOrEqual(8);
  });

  /* THE DEFECT THIS EXISTS FOR. "Audio routing" and "Two mixes, one
   * connection" both pointed at 02-routing.png, with the same alt text word
   * for word. A reader scrolling the page met the identical picture twice
   * under two different claims -- which reads as a product with one screen
   * and two names for it, and it shipped to the live site.
   *
   * Nothing could have caught it: the page renders correctly, the image
   * exists, the build passes, and the duplication is only visible to someone
   * scrolling the finished page. */
  it("gives every section a picture of its own", () => {
    const used = new Map<string, string[]>();
    for (const s of sections()) {
      used.set(s.shot, [...(used.get(s.shot) ?? []), s.id]);
    }
    const shared = [...used.entries()].filter(([, ids]) => ids.length > 1);
    expect(
      shared.map(([shot, ids]) => `${shot} is used by ${ids.join(" and ")}`),
      "two sections show the same screenshot, so the page claims two features " +
        "and illustrates one",
    ).toEqual([]);
  });

  /* Alt text is the half a sighted reader never sees, so a duplicate there
   * survives even a careful look at the rendered page. It was duplicated here
   * too, and it described the wrong screen for one of the two. */
  it("describes each picture in its own words", () => {
    const seen = new Map<string, string[]>();
    for (const s of sections()) {
      seen.set(s.alt, [...(seen.get(s.alt) ?? []), s.id]);
    }
    expect(
      [...seen.entries()].filter(([, ids]) => ids.length > 1).map(([, ids]) => ids.join(" and ")),
      "two sections share alt text, so a screen-reader user is told the same " +
        "thing about two different pictures",
    ).toEqual([]);
  });

  it("points only at screenshots that exist", () => {
    const have = new Set(readdirSync(join(here, "..", "assets", "shots")));
    const missing = sections().filter((s) => !have.has(s.shot));
    expect(
      missing.map((s) => `${s.id} -> ${s.shot}`),
      "a section names a screenshot that is not in src/assets/shots, so the " +
        "import glob resolves nothing and the figure renders empty",
    ).toEqual([]);
  });
});

/* THE SITE'S SCREENSHOTS MUST BE THE ONES THE CAPTURE PRODUCED.
 *
 * web/ keeps its own copies because Astro's image pipeline needs them under
 * src/assets to hash and optimise them; a path outside the project root cannot
 * be globbed, so a shared directory is not on offer. That left a manual copy as
 * the only link to docs/media, which scripts/capture-media.sh writes -- and a
 * manual copy is a step people forget.
 *
 * It was forgotten. The site shipped screenshots EIGHT DAYS OLDER than the
 * docs, showing an interface that had already changed, and ten of its eleven
 * files had drifted. Nothing said so: both sets existed, both looked plausible,
 * and the only way to notice was to compare them by hand.
 *
 * capture-media.sh now refreshes both. This is what catches the cases it
 * cannot: an image edited in place, a copy done for some files and not others,
 * a capture interrupted between the two writes.
 *
 * It compares BYTES rather than dimensions or mtimes. Two captures of the same
 * screen at the same size are still different pictures, which is the whole
 * defect -- and an mtime says when a file was written, not what is in it. */
describe("the site's screenshots match the capture output", () => {
  const shotsDir = join(here, "..", "assets", "shots");
  const docsDir = join(here, "..", "..", "..", "docs", "media");

  const siteShots = readdirSync(shotsDir).filter((f) => f.endsWith(".png"));

  it("has screenshots to check", () => {
    /* The positive control. Every assertion below passes over an empty list --
     * a renamed directory, a moved assets folder, a glob that stopped matching
     * -- and a green run over nothing is the failure this file exists to stop
     * repeating elsewhere. */
    expect(
      siteShots.length,
      "no screenshots found in src/assets/shots, so the comparison below " +
        "asserts nothing. Fix the path rather than accepting the pass.",
    ).toBeGreaterThan(0);
  });

  it.each(siteShots)("%s is byte-identical to docs/media", (name) => {
    const site = readFileSync(join(shotsDir, name));
    let docs: Buffer;
    try {
      docs = readFileSync(join(docsDir, name));
    } catch {
      throw new Error(
        `docs/media/${name} does not exist, but the site ships a screenshot ` +
          `by that name. Either the capture no longer produces it and the ` +
          `site's copy is orphaned, or it was added to the site by hand.`,
      );
    }
    expect(
      site.equals(docs),
      `src/assets/shots/${name} differs from docs/media/${name}. The capture ` +
        `writes docs/media and copies to the site; a difference means the copy ` +
        `did not happen, or one of them was edited in place. Re-run ` +
        `scripts/capture-media.sh rather than copying by hand — the two have ` +
        `already drifted eight days apart once.`,
    ).toBe(true);
  });
});
