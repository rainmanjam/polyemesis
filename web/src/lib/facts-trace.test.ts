import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CASES } from "../data/cases.mjs";
import { BY_SLUG } from "../data/docs.mjs";
import { docHref, docLinksOf, factProblems, fillCase, joinList } from "./facts";

/* lib/facts.ts: the traceability helper for the new pages. (The existing
 * facts.test.ts covers data/facts.mjs, a different module.) */

const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  if (!doc) return undefined;
  return readFileSync(fileURLToPath(new URL(`../../../docs/${doc.file}`, import.meta.url)), "utf8");
};

describe("joinList", () => {
  it("joins one, two and three items the way a sentence does", () => {
    expect(joinList(["a"])).toBe("a");
    expect(joinList(["a", "b"])).toBe("a and b");
    expect(joinList(["a", "b", "c"])).toBe("a, b and c");
  });
  it("joins nothing as an empty string", () => expect(joinList([])).toBe(""));
});

describe("fillCase", () => {
  it("fills the track list and the count", () => {
    expect(fillCase("Send {tracks} ({trackCount}).", CASES.sports)).toBe(
      "Send English, Español, Português, Français, Arabic and Crowd (6).",
    );
  });
  it("fills the destination names", () => {
    expect(fillCase("{dests}", CASES.sports).startsWith("YouTube · English,")).toBe(true);
  });
});

describe("docHref", () => {
  it("links the doc, with the anchor when there is one", () => {
    expect(docHref({ text: "", doc: "obs", evidence: ["x"], anchor: "multitrack-over-srt" })).toBe("/docs/obs#multitrack-over-srt");
    expect(docHref({ text: "", doc: "obs", evidence: ["x"] })).toBe("/docs/obs");
  });
});

describe("factProblems", () => {
  it("is empty for a fact the real doc supports", () => {
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["Output to URL", "mpegts"], anchor: "multitrack-over-srt" }], readDoc)).toEqual([]);
  });
  it("names a phrase that is in no document", () => {
    const p = factProblems([{ text: "t", doc: "obs", evidence: ["a phrase that is in no document"] }], readDoc);
    expect(p).toHaveLength(1);
    expect(p[0]).toContain("a phrase that is in no document");
  });
  it("names an unknown doc slug", () => {
    const p = factProblems([{ text: "t", doc: "nope", evidence: ["x"] }], readDoc);
    expect(p).toHaveLength(1);
    expect(p[0]).toContain("nope");
  });
  it("reports a doc the reader cannot read", () => {
    const p = factProblems([{ text: "t", doc: "obs", evidence: ["x"] }], () => undefined);
    expect(p[0]).toMatch(/could not be read/);
  });
  it("names an anchor that is not a heading of the doc", () => {
    const p = factProblems([{ text: "t", doc: "obs", evidence: ["mpegts"], anchor: "not-a-heading" }], readDoc);
    expect(p).toHaveLength(1);
    expect(p[0]).toContain("not-a-heading");
  });
  it("rejects a fact with no evidence", () => {
    expect(factProblems([{ text: "t", doc: "obs", evidence: [] }], readDoc)[0]).toMatch(/no evidence/);
  });
  it("matches through markdown emphasis, code, links and whitespace", () => {
    const doc = "# T\n\n## Heading\n\nIt is **measured *after* routing**, see [the `meters`\n  page](x.md).";
    const read = () => doc;
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["measured after routing", "see the meters page"], anchor: "heading" }], read)).toEqual([]);
  });
  it("matches case-insensitively", () => {
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["OUTPUT TO URL"] }], readDoc)).toEqual([]);
  });
});

describe("factProblems: evidence must be readable text of the doc", () => {
  const DOC = [
    "# Title",
    "",
    "Opening paragraph that wraps",
    "onto a second line.",
    "",
    "## Alpha",
    "",
    "- first item text",
    "- second item text",
    "",
    "| Setting | Value |",
    "| Container Format | `mpegts` |",
    "",
    "```",
    "code line one",
    "code line two",
    "```",
    "",
    "### Alpha detail",
    "",
    "Detail only here.",
    "",
    "## Beta",
    "",
    "Beta only text.",
  ].join("\n");
  const read = () => DOC;
  const check = (evidence: string[], anchor?: string) => factProblems([{ text: "t", doc: "obs", evidence, anchor }], read);

  it("rejects an evidence phrase that is empty once normalised", () => {
    for (const blank of ["", "  ", "*", "`_`"]) {
      const p = check([blank]);
      expect(p, JSON.stringify(blank)).toHaveLength(1);
      expect(p[0]).toMatch(/empty/);
    }
    expect(check(["Alpha", ""])).toHaveLength(1);
  });

  it("accepts a phrase that wraps across lines of one paragraph", () => {
    expect(check(["wraps onto a second line"])).toEqual([]);
  });

  it("rejects a phrase spanning a heading and the next paragraph", () => {
    expect(check(["Title Opening paragraph"])).toHaveLength(1);
  });
  it("rejects a phrase spanning two list items, two table rows, two code lines", () => {
    expect(check(["first item text second item text"])).toHaveLength(1);
    expect(check(["Setting Value Container Format"])).toHaveLength(1);
    expect(check(["code line one code line two"])).toHaveLength(1);
  });
  it("accepts a phrase inside one list item, one table row, one code line", () => {
    expect(check(["second item text", "Container Format", "code line two"])).toEqual([]);
  });

  it("rejects a phrase that starts or ends in the middle of a word", () => {
    expect(check(["lpha"])).toHaveLength(1);
    expect(check(["Alph"])).toHaveLength(1);
    expect(check(["tem text"])).toHaveLength(1);
    expect(check(["Alpha"])).toEqual([]);
  });
  it("finds a whole-word occurrence after a mid-word one", () => {
    const doc = "# T\n\nbeta-ray and a beta here.";
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["beta here"] }], () => doc)).toEqual([]);
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["eta here"] }], () => doc)).toHaveLength(1);
  });

  it("requires every evidence phrase of an anchored fact to sit in that section", () => {
    expect(check(["first item text", "Container Format"], "alpha")).toEqual([]);
    const p = check(["Beta only text"], "alpha");
    expect(p).toHaveLength(1);
    expect(p[0]).toContain("section");
    expect(p[0]).toContain("alpha");
  });
  it("lets an h2 section include its h3 subsections, and ends it at the next h2", () => {
    expect(check(["Detail only here"], "alpha")).toEqual([]);
    expect(check(["Detail only here"], "alpha-detail")).toEqual([]);
    expect(check(["second item text"], "alpha-detail")).toHaveLength(1);
    expect(check(["Beta only text"], "beta")).toEqual([]);
  });
  it("without an anchor, any block of the doc will do", () => {
    expect(check(["Beta only text", "Opening paragraph"])).toEqual([]);
  });
  it("keeps the plan's own example passing on the real OBS doc", () => {
    expect(factProblems([{ text: "t", doc: "obs", evidence: ["Output to URL", "mpegts"], anchor: "multitrack-over-srt" }], readDoc)).toEqual([]);
  });
});

describe("docLinksOf", () => {
  const f = (doc: string, anchor?: string) => ({ text: "t", doc, evidence: ["x"], anchor });
  it("gives one link per distinct doc, in order, to the first fact's anchor", () => {
    const links = docLinksOf([f("obs", "multitrack-over-srt"), f("quickstart"), f("obs", "other"), f("quickstart", "late")]);
    expect(links).toEqual([
      { href: "/docs/obs#multitrack-over-srt", text: BY_SLUG.get("obs")!.title },
      { href: "/docs/quickstart", text: BY_SLUG.get("quickstart")!.title },
    ]);
  });
  it("gives nothing for no facts and falls back to the slug for an unknown doc", () => {
    expect(docLinksOf([])).toEqual([]);
    expect(docLinksOf([f("nope")])).toEqual([{ href: "/docs/nope", text: "nope" }]);
  });
});
