import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { OG_CARDS } from "../data/og.mjs";

/* A link-preview card is the page's headline seen from outside, so its title must be the page's
 * own heading. The heading is read from the page's source: `heading="…"` on the InnerPage
 * element for the long-form pages, `DATA.heading` for the five /vs/* pages. A heading that
 * cannot be found fails the test; it never reads as "nothing to compare". */

type Card = { slug: string; path: string; title: string; accent?: string };
const cards = OG_CARDS as Card[];
const src = (p: string) => readFileSync(fileURLToPath(new URL(`../${p}`, import.meta.url)), "utf8");

/** Reads a string-literal prop written as `name="…"` at the start of a line (the InnerPage
 *  props sit one per line; the close band's are `name: "…"` object keys and never match). */
function literalProp(file: string, name: string): string | undefined {
  const text = src(file);
  const m = new RegExp(`^\\s*${name}="([^"]+)"`, "m").exec(text);
  if (m) return m[1];
  if (new RegExp(`^\\s*${name}=\\{`, "m").test(text)) {
    throw new Error(`${file}: ${name} is an expression, not a string literal; teach this test to read it`);
  }
  return undefined;
}

const card = (path: string) => {
  const c = cards.find((x) => x.path === path);
  if (!c) throw new Error(`no card for ${path}`);
  return c;
};

const countWord = (title: string, word: string) => {
  const esc = word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return (title.match(new RegExp(`(?<![\\p{L}\\p{N}])${esc}(?![\\p{L}\\p{N}])`, "gu")) ?? []).length;
};

const PAGES = [
  "download",
  "features",
  "calculator",
  "comparison",
  "multistream",
  "free-restream-service",
  "how-to-multistream-from-obs",
  "twitch-vod-track",
];
const VS = ["aitum-multistream", "obs-multi-rtmp", "restreamer", "streamelements", "streamlabs"];

/** Cards that are NOT the page heading. Each entry is the card title the route is expected to
 *  keep; the test fails if the card changes without this map, or if the page heading ever becomes
 *  equal to the entry (the entry is then stale). Only /vs/restreamer is deliberate (design section
 *  11a). The others are short link-preview headlines written before the pages were rebuilt, whose
 *  page headings are longer or different; they are listed for the owner to decide, not changed here. */
const CARD_DIFFERS: Record<string, string> = {
  "/vs/restreamer": "Two Restreams. Neither mixes per platform.",
  "/calculator": "Know your bandwidth before you go live.",
  "/multistream": "One upload to Twitch, YouTube and Kick.",
  "/free-restream-service": "A free restream service you run yourself.",
  "/how-to-multistream-from-obs": "How to multistream from OBS.",
  "/twitch-vod-track": "Different audio live and on the archive.",
  "/vs/aitum-multistream": "Mix tracks on the server, per destination.",
  "/vs/obs-multi-rtmp": "Upload once, fan out from a server.",
  "/vs/streamelements": "Multistream over SRT, from any operating system.",
  "/vs/streamlabs": "Your own relay, your own mix.",
};

type Heading = { heading?: string; accent?: string };
const pageHeading = (slug: string): Heading => {
  const file = `pages/${slug}.astro`;
  return { heading: literalProp(file, "heading"), accent: literalProp(file, "accent") };
};
const vsHeading = async (slug: string): Promise<Heading> => (await import(`../data/vs/${slug}.mjs`)).DATA;

const check = (path: string, { heading, accent }: Heading) => {
  const c = card(path);
  expect(heading, `${path}: page heading not found`).toBeTruthy();
  if (path in CARD_DIFFERS) {
    expect(c.title, `${path} card`).toBe(CARD_DIFFERS[path]);
    expect(c.title, `${path}: heading now equals the card; drop it from CARD_DIFFERS`).not.toBe(heading);
  } else {
    expect(c.title, `${path} card`).toBe(heading);
    if (accent) expect(c.accent, `${path} accent`).toBe(accent);
  }
  if (c.accent) expect(countWord(c.title, c.accent), `${path} accent occurs once`).toBe(1);
};

describe("card titles match page headings", () => {
  it.each(PAGES)("/%s", (p) => check(`/${p}`, pageHeading(p)));
  it.each(VS)("/vs/%s", async (s) => check(`/vs/${s}`, await vsHeading(s)));

  it("fails when a heading cannot be found", () => {
    expect(() => check("/download", {})).toThrow(/heading not found/);
  });
});
