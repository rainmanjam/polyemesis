import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CASES } from "../data/cases.mjs";
import { BY_SLUG } from "../data/docs.mjs";
import { docHref, factProblems, fillCase, joinList } from "./facts";

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
