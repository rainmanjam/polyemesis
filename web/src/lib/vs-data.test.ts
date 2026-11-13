import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DATA as aitum } from "../data/vs/aitum-multistream.mjs";
import { DATA as obs } from "../data/vs/obs-multi-rtmp.mjs";
import { DATA as restreamer } from "../data/vs/restreamer.mjs";
import { DATA as streamelements } from "../data/vs/streamelements.mjs";
import { DATA as streamlabs } from "../data/vs/streamlabs.mjs";
import { allowedTagProblems, validateVs, type VsData } from "./vs";

const ALL: VsData[] = [aitum, obs, restreamer, streamelements, streamlabs];
const bySlug = (slug: string) => ALL.find((d) => d.slug === slug) as VsData;

const fixture = JSON.parse(
  readFileSync(new URL("./legacy-inner.fixture.json", import.meta.url), "utf8"),
) as Record<string, { h1: string; text: string }>;
const comparisonMd = readFileSync(new URL("../../../docs/COMPARISON.md", import.meta.url), "utf8");

/* The same normalisation the build's copy check uses: tags to spaces, markdown
 * emphasis dropped, typographic dashes and quotes flattened, case folded. */
const norm = (s: string) =>
  s
    .replace(/<[^>]*>/g, " ")
    .replace(/[*`]/g, "")
    .replace(/[‐-―−]/g, "-")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();

const docText = norm(comparisonMd);
const legacyAll = norm(Object.values(fixture).map((f) => f.text).join(" "));

/** Every string in a value, however deep. */
function strings(v: unknown): string[] {
  if (typeof v === "string") return [v];
  if (Array.isArray(v)) return v.flatMap(strings);
  if (v && typeof v === "object") return Object.values(v).flatMap(strings);
  return [];
}

describe("the five /vs data files", () => {
  it.each(ALL.map((d) => [d.slug, d] as const))("%s passes validateVs", (_slug, d) => {
    expect(validateVs(d)).toEqual([]);
  });

  it("has a which block exactly on restreamer, streamelements and streamlabs", () => {
    const have = ALL.filter((d) => d.which).map((d) => d.slug).sort((a, b) => a.localeCompare(b));
    expect(have).toEqual(["restreamer", "streamelements", "streamlabs"]);
  });

  it("has a use-the-other list exactly on aitum, obs-multi-rtmp, streamelements and streamlabs", () => {
    const have = ALL.filter((d) => d.useOther).map((d) => d.slug).sort((a, b) => a.localeCompare(b));
    expect(have).toEqual(["aitum-multistream", "obs-multi-rtmp", "streamelements", "streamlabs"]);
  });

  it("has each page's middle section", () => {
    const find = (d: VsData, kind: string, word: string) =>
      d.sections.some((s) => s.kind === kind && s.heading.toLowerCase().includes(word));
    expect(find(restreamer, "table", "cost")).toBe(true);
    expect(find(obs, "table", "leaves")).toBe(true);
    expect(aitum.sections.some((s) => s.kind === "steps")).toBe(true);
    expect(find(streamelements, "items", "limits")).toBe(true);
  });

  it("has two products on restreamer and one elsewhere", () => {
    expect(restreamer.products).toHaveLength(2);
    for (const d of ALL.filter((x) => x.slug !== "restreamer")) expect(d.products).toHaveLength(1);
  });

  it.each(ALL.map((d) => [d.slug, d] as const))("%s has a full glance table", (_slug, d) => {
    expect(d.glance.length).toBeGreaterThanOrEqual(3);
    for (const row of d.glance) expect(row).toHaveLength(2 + d.products.length);
  });

  it.each(ALL.map((d) => [d.slug, d] as const))("%s heading is the legacy h1", (slug, d) => {
    expect(norm(d.heading)).toBe(norm(fixture[`/vs/${slug}`].h1));
  });

  describe("facts rule: nothing about another product that is not already written", () => {
    it.each(ALL.map((d) => [d.slug, d] as const))("%s", (slug, d) => {
      const own = norm(fixture[`/vs/${slug}`].text);
      const known = (s: string, extra = "") => {
        const n = norm(s);
        return own.includes(n) || docText.includes(n) || (extra !== "" && extra.includes(n));
      };
      const unsupported: string[] = [];
      for (const row of d.glance) {
        row.slice(2).forEach((cell) => known(cell) || unsupported.push(`glance: ${cell}`));
        // polyemesis's own column may also be written on the other pages.
        known(row[1], legacyAll) || unsupported.push(`glance (polyemesis): ${row[1]}`);
      }
      for (const s of [...d.fits.poly, ...d.fits.other.items]) known(s) || unsupported.push(`fits: ${s}`);
      for (const s of strings(d.merits?.items)) known(s) || unsupported.push(`merits: ${s}`);
      for (const s of strings(d.useOther?.items)) known(s) || unsupported.push(`useOther: ${s}`);
      expect(unsupported).toEqual([]);
    });
  });

  it("uses only the four allowed tags and never says pre-release", () => {
    for (const d of ALL) {
      for (const s of strings(d)) {
        expect(allowedTagProblems(s), s).toEqual([]);
        expect(s.toLowerCase()).not.toContain("pre-release");
      }
    }
  });
});

describe("validateVs", () => {
  const clone = (): VsData => structuredClone(bySlug("aitum-multistream"));
  const problems = (d: VsData) => validateVs(d).join("\n");

  it("accepts a copy of a good page", () => {
    expect(validateVs(clone())).toEqual([]);
  });

  it("rejects a missing required string", () => {
    const d = clone();
    d.lede = "  ";
    expect(problems(d)).toContain("lede");
  });

  it("rejects an unknown slug", () => {
    const d = clone();
    (d as { slug: string }).slug = "nope";
    expect(problems(d)).toContain("slug");
  });

  it("rejects an accent that is not in the heading, or is in it twice", () => {
    const d = clone();
    d.accent = "zebra";
    expect(problems(d)).toContain("accent");
    d.heading = "word word";
    d.accent = "word";
    expect(problems(d)).toContain("accent");
  });

  it("rejects zero or three products, an empty key, a reused key and polyemesis as a key", () => {
    const d = clone();
    d.products = [];
    expect(problems(d)).toContain("products");
    d.products = [
      { key: "a", name: "A" },
      { key: "b", name: "B" },
      { key: "c", name: "C" },
    ];
    expect(problems(d)).toContain("products");
    d.products = [
      { key: "", name: "A" },
      { key: "polyemesis", name: "B" },
    ];
    expect(problems(d)).toContain("key");
    d.products = [
      { key: "x", name: "A" },
      { key: "x", name: "B" },
    ];
    expect(problems(d)).toContain("twice");
  });

  it("rejects fewer than three glance rows and a row of the wrong width", () => {
    const d = clone();
    d.glance = d.glance.slice(0, 2);
    expect(problems(d)).toContain("glance needs at least 3 rows");
    d.glance = [...d.glance, ["a", "b"], ["c", "d", "e", "f"]];
    expect(problems(d)).toContain("glance row");
  });

  it("rejects empty fits lists", () => {
    const d = clone();
    d.fits = { poly: [], other: { heading: "x", items: [] } };
    expect(problems(d)).toContain("fits.poly");
    expect(problems(d)).toContain("fits.other");
  });

  it("rejects a which block with no items", () => {
    const d = structuredClone(restreamer);
    d.which = { heading: "x", items: [] };
    expect(problems(d)).toContain("which");
  });

  it("rejects empty merits and an empty use-other list", () => {
    const d = clone();
    d.merits = { heading: "x", items: [] };
    d.useOther = { heading: "x", items: [] };
    expect(problems(d)).toContain("merits");
    expect(problems(d)).toContain("useOther");
  });

  it("rejects bad section ids", () => {
    const d = clone();
    d.sections = [
      { kind: "prose", id: "Bad Id", heading: "h", paragraphs: ["p"] },
      { kind: "prose", id: "glance", heading: "h", paragraphs: ["p"] },
      { kind: "prose", id: "same", heading: "h", paragraphs: ["p"] },
      { kind: "prose", id: "same", heading: "h", paragraphs: ["p"] },
    ];
    const text = problems(d);
    expect(text).toContain("Bad Id");
    expect(text).toContain("reserved");
    expect(text).toContain("used twice");
  });

  it("rejects each section kind that has no content", () => {
    const d = clone();
    d.sections = [
      { kind: "prose", id: "a", heading: "h", paragraphs: [] },
      { kind: "table", id: "b", heading: "h", caption: "c", head: ["x", "y"], rows: [["1"]] },
      { kind: "steps", id: "c", heading: "h", columns: [] },
      { kind: "steps", id: "d", heading: "h", columns: [{ title: "t", steps: [] }] },
      { kind: "items", id: "e", heading: "h", items: [] },
    ];
    const text = problems(d);
    expect(text).toContain("section a");
    expect(text).toContain("section b");
    expect(text).toContain("section c");
    expect(text).toContain("section d");
    expect(text).toContain("section e");
  });

  it("rejects a script tag and an inline handler anywhere in the data", () => {
    const d = clone();
    d.lede = 'fine <script>alert(1)</script>';
    (d.merits as NonNullable<VsData["merits"]>).items[0].p = '<a href="/x" onclick="x()">link</a>';
    const text = problems(d);
    expect(text).toContain("lede");
    expect(text).toContain("merits");
  });

  it("rejects pre-release", () => {
    const d = clone();
    d.lede = "still Pre-release";
    expect(problems(d)).toContain("pre-release");
  });
});

describe("allowedTagProblems", () => {
  it("allows a, code, em and strong, and plain text", () => {
    expect(allowedTagProblems('x <a href="/vs/restreamer">y</a> <code>z</code> <em>e</em> <strong>s</strong>')).toEqual([]);
    expect(allowedTagProblems("1 < 2 is not a tag")).toEqual([]);
  });

  it("allows https and relative and fragment hrefs only", () => {
    expect(allowedTagProblems('<a href="https://example.com/x">y</a>')).toEqual([]);
    expect(allowedTagProblems('<a href="#glance">y</a>')).toEqual([]);
    expect(allowedTagProblems('<a href="javascript:alert(1)">y</a>')).not.toEqual([]);
    expect(allowedTagProblems("<a>y</a>")).not.toEqual([]);
  });

  it("rejects other tags, attributes, and an unterminated tag", () => {
    expect(allowedTagProblems("<script>x</script>")).not.toEqual([]);
    expect(allowedTagProblems('<em onclick="x">y</em>')).not.toEqual([]);
    expect(allowedTagProblems('<code class="x">y</code>')).not.toEqual([]);
    expect(allowedTagProblems("<img src=x")).not.toEqual([]);
  });
});
