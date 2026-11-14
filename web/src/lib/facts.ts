/* Facts for the new pages: every product statement carries the doc it comes
 * from and the phrases that prove it. factProblems() is what a test (and the
 * build) runs to keep a sentence from outliving the doc that supported it.
 *
 * Names in a diagram come from cases.mjs only; a page writes {tracks},
 * {dests} and {trackCount} and fillCase() fills them. */
import { BY_SLUG } from "../data/docs.mjs";
import { extractHeadings } from "./doc-headings";

/** The part of an example case the text fill reads. */
export interface Case {
  tracks: { id: string; label: string }[];
  dests: { name: string }[];
}

/** `doc` is a slug in docs.mjs. */
export interface Fact {
  text: string;
  doc: string;
  evidence: string[];
  anchor?: string;
}
export interface StepShot {
  file: string;
  alt: string;
  chrome?: string;
  caption?: string;
}
export interface Step {
  title: string;
  facts: Fact[];
  shot?: StepShot;
}

/** ["a"] "a"; ["a","b"] "a and b"; ["a","b","c"] "a, b and c". */
export function joinList(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

export function fillCase(text: string, c: Case): string {
  return text
    .replaceAll("{trackCount}", String(c.tracks.length))
    .replaceAll("{tracks}", joinList(c.tracks.map((t) => t.label)))
    .replaceAll("{dests}", joinList(c.dests.map((d) => d.name)));
}

export function docHref(f: Fact): string {
  return `/docs/${f.doc}${f.anchor ? `#${f.anchor}` : ""}`;
}

/** Markdown reduced to the words a reader sees, lower-cased, one space between words. */
function normalise(s: string): string {
  return s
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[*_`]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();
}

/** One message per problem; [] when every fact is traceable. */
export function factProblems(facts: Fact[], readDoc: (slug: string) => string | undefined): string[] {
  const problems: string[] = [];
  for (const f of facts) {
    const label = `"${f.text}"`;
    if (f.evidence.length === 0) problems.push(`${label}: no evidence phrases`);
    if (!BY_SLUG.has(f.doc)) {
      problems.push(`${label}: doc "${f.doc}" is not a published doc slug`);
      continue;
    }
    const md = readDoc(f.doc);
    if (md === undefined) {
      problems.push(`${label}: doc "${f.doc}" could not be read`);
      continue;
    }
    const body = normalise(md);
    for (const phrase of f.evidence) {
      if (!body.includes(normalise(phrase))) problems.push(`${label}: evidence "${phrase}" is not in doc "${f.doc}"`);
    }
    if (f.anchor !== undefined && !extractHeadings(md).some((h) => h.slug === f.anchor)) {
      problems.push(`${label}: anchor "${f.anchor}" is not a heading of doc "${f.doc}"`);
    }
  }
  return problems;
}
