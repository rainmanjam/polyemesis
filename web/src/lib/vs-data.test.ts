import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DATA as aitum } from "../data/vs/aitum-multistream.mjs";
import { DATA as obs } from "../data/vs/obs-multi-rtmp.mjs";
import { DATA as restreamer } from "../data/vs/restreamer.mjs";
import { DATA as streamelements } from "../data/vs/streamelements.mjs";
import { DATA as streamlabs } from "../data/vs/streamlabs.mjs";
import { allowedTagProblems, hrefProblem, validateVs, type VsData } from "./vs";

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
    /* A string of four or more words must occur whole in the page's frozen text
     * or in docs/COMPARISON.md. A shorter one matches almost anything as a
     * substring ("Yes" is inside every page), so it must either be a whole table
     * cell or list item of COMPARISON.md, or be listed here with the old-page
     * sentence or document line it stands for. Keys are normalised. */
    const SHORT_CELLS: Record<string, string> = {
      "actively maintained": "obs-multi-rtmp merit heading, as the old page had it (card title over the sorayuki sentence).",
      "free, and gpl-2.0": "aitum merit heading and the plugin's cost, from the old note dated 15 August 2026.",
      "free, gpl-2.0": "obs-multi-rtmp cost: the old merit heading \"Free, GPL-2.0, and inside OBS already\".",
      "free, mit, self-hosted": "polyemesis's own price: the old restreamer pricing row for polyemesis (another page's text, allowed for column 1).",
      "free, apache-2.0, self-hosted": "Restreamer's price: the old restreamer pricing row \"Restreamer (datarhei)\".",
      "one per destination": "obs-multi-rtmp outbound streams / audio encodes: the old outbound table cell.",
      "the server": "aitum: the old \"The mixing desk is the server\" sentence.",
      "multitrack srt": "polyemesis's transport: old streamelements sentence \"receiving your separated audio tracks over multitrack SRT\".",
      "multitrack srt in": "polyemesis's ingest: old streamlabs heading \"RTMP in, or multitrack SRT in\".",
      "rtmp in": "Streamlabs's ingest: same old heading.",
      "their servers": "Streamlabs: old streamlabs heading \"Their servers, or yours\".",
      "yours": "polyemesis: same old heading.",
      "per-destination encoding": "Aitum merit heading, as the old page had it.",
      "restream.io keeps shipping": "restream.io merit heading, as the old page had it.",
      "streamsuite": "Aitum merit heading, as the old page had it.",
    };
    const docCells = new Set<string>();
    for (const line of comparisonMd.split("\n")) {
      if (line.startsWith("|")) {
        for (const c of line.slice(1).split("|")) docCells.add(norm(c.replace(/<sup>\d<\/sup>/g, "")));
      } else if (line.startsWith("- ")) {
        docCells.add(norm(line.slice(2)));
      }
    }
    const used = new Set<string>();

    it.each(ALL.map((d) => [d.slug, d] as const))("%s", (slug, d) => {
      const own = norm(fixture[`/vs/${slug}`].text);
      const unsupported: string[] = [];
      const check = (where: string, s: string, extra = "") => {
        const n = norm(s);
        if (n === "") return void unsupported.push(`${where}: empty`);
        const written = own.includes(n) || docText.includes(n) || (extra !== "" && extra.includes(n));
        if (!written) return void unsupported.push(`${where}: ${s}`);
        if (n.split(" ").length >= 4) return;
        if (docCells.has(n)) return;
        if (n in SHORT_CELLS) return void used.add(n);
        unsupported.push(`${where}: short string needs SHORT_CELLS or a whole doc cell: ${s}`);
      };
      for (const row of d.glance) {
        row.slice(2).forEach((cell) => check("glance", cell));
        // polyemesis's own column may also be written on the other pages.
        check("glance (polyemesis)", row[1], legacyAll);
      }
      for (const s of [...d.fits.poly, ...d.fits.other.items]) check("fits", s);
      for (const s of strings(d.merits?.items)) check("merits", s);
      for (const s of strings(d.useOther?.items)) check("useOther", s);
      expect(unsupported).toEqual([]);
    });

    it("has no stale SHORT_CELLS entry", () => {
      expect(Object.keys(SHORT_CELLS).filter((k) => !used.has(k))).toEqual([]);
    });
  });

  it("has merits on every page but streamlabs", () => {
    expect(ALL.filter((d) => !d.merits).map((d) => d.slug)).toEqual(["streamlabs"]);
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

describe("validateVs hardening", () => {
  const full = () => structuredClone(restreamer);
  const problems = (d: VsData) => validateVs(d).join("\n");
  const evil = "<script>alert(1)</script>";

  it("finds an attack in every kind of nested string", () => {
    const cases: [string, (d: VsData) => void, string][] = [
      ["prose", (d) => (d.sections[0] = { kind: "prose", id: "p", heading: "h", paragraphs: [evil] }), "sections[0].paragraphs[0]"],
      ["table", (d) => (d.sections[0] = { kind: "table", id: "t", heading: "h", caption: "c", head: ["a", "b"], rows: [["x", evil]] }), "sections[0].rows[0][1]"],
      ["steps", (d) => (d.sections[0] = { kind: "steps", id: "s", heading: "h", columns: [{ title: "t", steps: [evil] }] }), "sections[0].columns[0].steps[0]"],
      ["items", (d) => (d.sections[0] = { kind: "items", id: "i", heading: "h", items: [{ h: "h", p: "p", more: evil }] }), "sections[0].items[0].more"],
      ["which", (d) => ((d.which as NonNullable<VsData["which"]>).items[0].body = evil), "which.items[0].body"],
      ["closing", (d) => (d.closing.lede = evil), "closing.lede"],
      ["footer", (d) => (d.footer = evil), "footer"],
      ["after", (d) => (d.sections[0].after = ['<a href="x" onclick="y">z</a>']), "sections[0].after[0]"],
    ];
    for (const [name, mutate, path] of cases) {
      const d = full();
      mutate(d);
      expect(problems(d), name).toContain(path);
    }
  });

  it("rejects comments, processing instructions and CDATA", () => {
    for (const s of ["a <!-- b -->", "a <?php x ?>", "a <![CDATA[ b ]]>"]) {
      expect(allowedTagProblems(s), s).not.toEqual([]);
    }
  });

  it("rejects unbalanced, mismatched and nested tags", () => {
    expect(allowedTagProblems("<em>never closed")).not.toEqual([]);
    expect(allowedTagProblems("never opened</em>")).not.toEqual([]);
    expect(allowedTagProblems("<em>a</strong>")).not.toEqual([]);
    expect(allowedTagProblems('<a href="/x">a <a href="/y">b</a></a>')).not.toEqual([]);
    expect(allowedTagProblems("<strong>a <em>b</em></strong>")).toEqual([]);
  });

  it("rejects any tag in a field the template shows as plain text", () => {
    const plain: [(d: VsData) => void, string][] = [
      [(d) => (d.glance[0][1] = "<em>x</em>"), "glance[0][1]"],
      [(d) => (d.glance[0][0] = "<em>x</em>"), "glance[0][0]"],
      [(d) => (d.heading = "a <em>b</em>"), "heading"],
      [(d) => (d.lede = "a <code>b</code>"), "lede"],
      [(d) => (d.sections[2] = { kind: "table", id: "t", heading: "h", caption: "c", head: ["x", "<em>y</em>"], rows: [["a", "<em>b</em>"]] }), "sections[2].head[1]"],
      [(d) => (d.closing.lede = "a <em>b</em>"), "closing.lede"],
      [(d) => (d.closing.heading = "a <em>b</em>"), "closing.heading"],
      [(d) => (d.merits as NonNullable<VsData["merits"]>).items[0].h = "<em>h</em>", "merits.items[0].h"],
      [(d) => ((d.which as NonNullable<VsData["which"]>).items[0].name = "<em>n</em>"), "which.items[0].name"],
    ];
    for (const [mutate, path] of plain) {
      const d = full();
      mutate(d);
      expect(problems(d), path).toContain(`${path}: a tag is not allowed here`);
    }
  });

  it("still allows the four tags in the fields rendered as HTML", () => {
    const d = full();
    d.footer = "see <code>x</code> and <a href=\"/y\">y</a>";
    d.callout = { lead: "Lead", body: "a <em>b</em> <strong>c</strong>" };
    expect(validateVs(d)).toEqual([]);
  });

  it("rejects a table section whose first header is Capability", () => {
    const d = full();
    d.sections[2] = { kind: "table", id: "t", heading: "h", caption: "c", head: [" capability ", "b"], rows: [["x", "y"]] };
    expect(problems(d)).toContain("Capability");
  });

  it("validates the href of every link the template renders", () => {
    const bad = ["javascript:alert(1)", "data:text/html,x", "//evil.example/x", "http://example.com", "", " /x"];
    for (const href of bad) {
      const d = structuredClone(bySlug("streamlabs"));
      (d.useOther as NonNullable<VsData["useOther"]>).links = [{ label: "x", href }];
      expect(problems(d), href).toContain("useOther.links[0].href");
    }
    for (const href of ["/docs/x", "https://example.com/x", "#glance"]) {
      const d = structuredClone(bySlug("streamlabs"));
      (d.useOther as NonNullable<VsData["useOther"]>).links = [{ label: "x", href }];
      expect(validateVs(d), href).toEqual([]);
    }
  });
});

describe("hrefProblem", () => {
  it("accepts a path, a fragment and https, and nothing else", () => {
    expect(hrefProblem("/docs/x")).toBeNull();
    expect(hrefProblem("#a")).toBeNull();
    expect(hrefProblem("https://example.com")).toBeNull();
    for (const h of ["javascript:x", "data:x", "//evil", "http://x", "mailto:a@b", ""]) {
      expect(hrefProblem(h), h).not.toBeNull();
    }
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
