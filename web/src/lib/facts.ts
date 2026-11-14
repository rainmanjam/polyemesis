/* Facts for the new pages: every product statement carries the doc it comes
 * from and the phrases that tie it to that doc. factProblems() is what a test
 * (and the build) runs to keep a sentence from outliving the doc that supported
 * it. It proves a phrase is in the doc's section, not that the sentence is
 * true: a negated sentence in the doc, or a heading's own words, would pass, so
 * a person reads each sentence against that paragraph.
 *
 * Names in a diagram come from cases.mjs only; a page writes {tracks},
 * {dests} and {trackCount} and fillCase() fills them. */
import { BY_SLUG } from "../data/docs.mjs";
import { extractHeadingsAt } from "./doc-headings";

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

/** One link per distinct doc a list of facts comes from, in order, to the first fact's anchor. */
export function docLinksOf(facts: Fact[]): { href: string; text: string }[] {
  const first = new Map<string, Fact>();
  for (const f of facts) if (!first.has(f.doc)) first.set(f.doc, f);
  return [...first.values()].map((f) => ({ href: docHref(f), text: BY_SLUG.get(f.doc)?.title ?? f.doc }));
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

interface Block {
  /** 0-based line the block starts on. */
  line: number;
  text: string;
}

const HEADING_LINE = /^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const ITEM_OR_QUOTE = /^ {0,3}(?:[-*+]|\d+[.)]|>)[ \t]+/;

/** The doc as the blocks a phrase may sit in: a heading, a paragraph (its wrapped
 *  lines joined), a list item, a table row or a code line. `headings` are the
 *  lines that start a section, with their level. */
function blocksOf(md: string): { blocks: Block[]; headings: { line: number; level: number }[] } {
  const blocks: Block[] = [];
  const headings: { line: number; level: number }[] = [];
  let fence: string | null = null;
  let open: Block | null = null;
  const close = () => {
    if (open) blocks.push(open);
    open = null;
  };
  for (const [line, raw] of md.split(/\r?\n/).entries()) {
    const f = FENCE.exec(raw);
    if (fence) {
      if (f && f[1][0] === fence[0] && f[1].length >= fence.length && raw.trim() === f[1]) fence = null;
      else blocks.push({ line, text: raw });
      continue;
    }
    if (f) {
      close();
      fence = f[1];
      continue;
    }
    if (raw.trim() === "") {
      close();
      continue;
    }
    const h = HEADING_LINE.exec(raw);
    if (h) {
      close();
      headings.push({ line, level: h[1].length });
      blocks.push({ line, text: h[2] });
      continue;
    }
    if (raw.trimStart().startsWith("|") || ITEM_OR_QUOTE.test(raw)) {
      close();
      if (raw.trimStart().startsWith("|")) blocks.push({ line, text: raw });
      else open = { line, text: raw.replace(ITEM_OR_QUOTE, "") };
      continue;
    }
    if (open) open.text += `\n${raw}`;
    else open = { line, text: raw };
  }
  close();
  return { blocks, headings };
}

const isWordChar = (c: string | undefined) => c !== undefined && /[\p{L}\p{N}]/u.test(c);

/** True when `phrase` occurs in `block` starting and ending on a word boundary. */
function occursIn(block: string, phrase: string): boolean {
  for (let i = block.indexOf(phrase); i >= 0; i = block.indexOf(phrase, i + 1)) {
    const startsMid = isWordChar(phrase[0]) && isWordChar(block[i - 1]);
    const endsMid = isWordChar(phrase[phrase.length - 1]) && isWordChar(block[i + phrase.length]);
    if (!startsMid && !endsMid) return true;
  }
  return false;
}

/** One message per problem; [] when every fact is traceable.
 *
 *  A phrase must sit inside ONE block of the doc (never across two), on word
 *  boundaries, and, when the fact has an anchor, inside that section: from the
 *  anchor's heading to the next heading of the same or a higher level. */
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
    const { blocks, headings } = blocksOf(md);
    const anchorHeading = f.anchor === undefined ? undefined : extractHeadingsAt(md).find((h) => h.slug === f.anchor);
    if (f.anchor !== undefined && !anchorHeading) problems.push(`${label}: anchor "${f.anchor}" is not a heading of doc "${f.doc}"`);

    let scope = blocks;
    if (anchorHeading) {
      const end = headings.find((h) => h.line > anchorHeading.line && h.level <= anchorHeading.depth)?.line ?? Infinity;
      scope = blocks.filter((b) => b.line >= anchorHeading.line && b.line < end);
    }
    const texts = scope.map((b) => normalise(b.text));
    for (const phrase of f.evidence) {
      const want = normalise(phrase);
      if (want === "") problems.push(`${label}: evidence phrase ${JSON.stringify(phrase)} is empty`);
      else if (!texts.some((t) => occursIn(t, want))) {
        problems.push(
          anchorHeading
            ? `${label}: evidence "${phrase}" is not in the section "${f.anchor}" of doc "${f.doc}"`
            : `${label}: evidence "${phrase}" is not in doc "${f.doc}"`,
        );
      }
    }
  }
  return problems;
}
