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

interface Heading {
  line: number;
  level: number;
}

/** What blocksOf carries from line to line: the blocks so far, the section starts, the fence that is open (its
 *  opening run of ` or ~) and the block still being built. */
interface Scan {
  blocks: Block[];
  headings: Heading[];
  fence: string | null;
  open: Block | null;
}

/** Ends the block being built, if any. */
function closeBlock(st: Scan): void {
  if (st.open) st.blocks.push(st.open);
  st.open = null;
}

/** A line inside a fence is a block of its own, unless it closes the fence: the same character, at least as
 *  long as the opening run, and nothing else on the line. */
function scanFenced(st: Scan, line: number, raw: string, fence: string): void {
  const f = FENCE.exec(raw);
  const closes = f !== null && f[1][0] === fence[0] && f[1].length >= fence.length && raw.trim() === f[1];
  if (closes) st.fence = null;
  else st.blocks.push({ line, text: raw });
}

function scanHeading(st: Scan, line: number, level: number, text: string): void {
  closeBlock(st);
  st.headings.push({ line, level });
  st.blocks.push({ line, text });
}

/** A table row is a block of its own; a list item or a quote line starts one (its text without the marker). */
function scanRowOrItem(st: Scan, line: number, raw: string): void {
  closeBlock(st);
  if (raw.trimStart().startsWith("|")) st.blocks.push({ line, text: raw });
  else st.open = { line, text: raw.replace(ITEM_OR_QUOTE, "") };
}

/** A wrapped line joins the block being built; otherwise it starts a paragraph. */
function scanText(st: Scan, line: number, raw: string): void {
  if (st.open) st.open.text += `\n${raw}`;
  else st.open = { line, text: raw };
}

function startsRowOrItem(raw: string): boolean {
  return raw.trimStart().startsWith("|") || ITEM_OR_QUOTE.test(raw);
}

function scanLine(st: Scan, line: number, raw: string): void {
  if (st.fence !== null) return scanFenced(st, line, raw, st.fence);
  const f = FENCE.exec(raw);
  if (f) {
    closeBlock(st);
    st.fence = f[1];
    return;
  }
  if (raw.trim() === "") return closeBlock(st);
  const h = HEADING_LINE.exec(raw);
  if (h) return scanHeading(st, line, h[1].length, h[2]);
  if (startsRowOrItem(raw)) return scanRowOrItem(st, line, raw);
  scanText(st, line, raw);
}

/** The doc as the blocks a phrase may sit in: a heading, a paragraph (its wrapped
 *  lines joined), a list item, a table row or a code line. `headings` are the
 *  lines that start a section, with their level. */
function blocksOf(md: string): { blocks: Block[]; headings: Heading[] } {
  const st: Scan = { blocks: [], headings: [], fence: null, open: null };
  for (const [line, raw] of md.split(/\r?\n/).entries()) scanLine(st, line, raw);
  closeBlock(st);
  return { blocks: st.blocks, headings: st.headings };
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

/** The blocks of the section that starts at `anchor`: from its heading to the next heading of the same or a
 *  higher level. Without an anchor, the whole doc. */
function sectionBlocks(blocks: Block[], headings: Heading[], anchor: { line: number; depth: number } | undefined): Block[] {
  if (!anchor) return blocks;
  const end = headings.find((h) => h.line > anchor.line && h.level <= anchor.depth)?.line ?? Infinity;
  return blocks.filter((b) => b.line >= anchor.line && b.line < end);
}

/** The problem with one evidence phrase against the texts it may sit in, or undefined when it is there. */
function phraseProblem(f: Fact, label: string, texts: string[], phrase: string, inSection: boolean): string | undefined {
  const want = normalise(phrase);
  if (want === "") return `${label}: evidence phrase ${JSON.stringify(phrase)} is empty`;
  if (texts.some((t) => occursIn(t, want))) return undefined;
  return inSection
    ? `${label}: evidence "${phrase}" is not in the section "${f.anchor}" of doc "${f.doc}"`
    : `${label}: evidence "${phrase}" is not in doc "${f.doc}"`;
}

/** The problems of a fact whose doc is published and was read: its anchor, then each evidence phrase. */
function docProblems(f: Fact, label: string, md: string): string[] {
  const { blocks, headings } = blocksOf(md);
  const anchorHeading = f.anchor === undefined ? undefined : extractHeadingsAt(md).find((h) => h.slug === f.anchor);
  const problems: string[] = [];
  if (f.anchor !== undefined && !anchorHeading) problems.push(`${label}: anchor "${f.anchor}" is not a heading of doc "${f.doc}"`);
  const texts = sectionBlocks(blocks, headings, anchorHeading).map((b) => normalise(b.text));
  for (const phrase of f.evidence) {
    const problem = phraseProblem(f, label, texts, phrase, anchorHeading !== undefined);
    if (problem) problems.push(problem);
  }
  return problems;
}

/** The problems of one fact: no evidence, a doc that is not published or cannot be read, or what docProblems finds. */
function factProblemsOf(f: Fact, readDoc: (slug: string) => string | undefined): string[] {
  const label = `"${f.text}"`;
  const problems = f.evidence.length === 0 ? [`${label}: no evidence phrases`] : [];
  if (!BY_SLUG.has(f.doc)) return [...problems, `${label}: doc "${f.doc}" is not a published doc slug`];
  const md = readDoc(f.doc);
  if (md === undefined) return [...problems, `${label}: doc "${f.doc}" could not be read`];
  return [...problems, ...docProblems(f, label, md)];
}

/** One message per problem; [] when every fact is traceable.
 *
 *  A phrase must sit inside ONE block of the doc (never across two), on word
 *  boundaries, and, when the fact has an anchor, inside that section: from the
 *  anchor's heading to the next heading of the same or a higher level. It proves
 *  the phrase is in that section, not that the sentence is true. */
export function factProblems(facts: Fact[], readDoc: (slug: string) => string | undefined): string[] {
  return facts.flatMap((f) => factProblemsOf(f, readDoc));
}
