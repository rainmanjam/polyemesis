/* The headings of a docs page, read from its markdown source, for the "On this
 * page" list. Slugs come from the same uniqueSlugger() as the pipeline plugin
 * (hast-heading-ids.mjs), so each entry links to the id the built page carries.
 */
import { uniqueSlugger } from "./heading-slug.mjs";

export interface DocHeading {
  depth: 2 | 3;
  text: string;
  slug: string;
}

const HEADING = /^ {0,3}(#{2,3})[ \t]+(.*?)[ \t]*$/;
// CommonMark's block-level HTML start conditions: the type 6 tag names (opening
// or closing), plus <pre, <script, <style and comments. An inline tag such as
// <kbd> or <a> at the start of a line is a paragraph, not a block.
const BLOCK_TAGS =
  "address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|" +
  "fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|" +
  "menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|" +
  "track|ul";
const HTML_BLOCK_OPEN = new RegExp(
  `^ {0,3}(?:<!--|<(?:pre|script|style)(?=[\\s>]|$)|</?(?:${BLOCK_TAGS})(?=[\\s>]|/>|$))`,
  "i",
);
const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/;

const NAMED_ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" };

/** Decodes the entities a heading can carry: &amp; &lt; &gt; &quot; &apos;, &#39;, &#x27;. */
function decodeEntities(text: string): string {
  return text.replace(/&(?:#(\d+)|#[xX]([0-9a-fA-F]+)|([a-z]+));/g, (whole, dec, hex, name) => {
    if (name) return NAMED_ENTITIES[name] ?? whole;
    const cp = dec ? Number(dec) : parseInt(hex, 16);
    // Beyond U+10FFFF there is no character: leave the entity as written.
    return cp <= 0x10ffff ? String.fromCodePoint(cp) : whole;
  });
}

const ESCAPE_BASE = 0xe000;

/** Reduces inline markdown to the text a reader sees. */
function plainText(raw: string): string {
  // A backslash before ASCII punctuation is an escape: kept as a private-use
  // placeholder while the emphasis rules run, then turned into the character.
  const escaped = decodeEntities(raw).replace(/\\([!-/:-@[-`{-~])/g, (_, c: string) =>
    String.fromCodePoint(ESCAPE_BASE + c.charCodeAt(0)),
  );
  return escaped
    .replace(/[ \t]+#+$/, "") // closing run of #
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1") // links and images
    .replace(/`+([^`]*)`+/g, "$1") // inline code
    .replace(/\*{1,3}([^*]+)\*{1,3}/g, "$1") // * emphasis
    .replace(/(?<!\w)_{1,3}([^_]+)_{1,3}(?!\w)/g, "$1") // _ emphasis, not snake_case
    .replace(/[\uE000-\uE07F]/g, (c) => String.fromCodePoint(c.codePointAt(0)! - ESCAPE_BASE))
    .trim();
}

/** ATX `##` and `###` headings outside fenced code, in document order.
 *
 *  THE LIMIT IS DELIBERATE: only top-level ATX headings are read. Setext
 *  headings (underlined with === or ---), headings inside block quotes or
 *  lists, and headings inside HTML blocks are not, although the page's ids
 *  cover them. A build check that the list equals the article's headings is
 *  what catches a document that starts using one. */
export function extractHeadings(markdown: string): DocHeading[] {
  const slug = uniqueSlugger();
  const headings: DocHeading[] = [];
  let fence: string | null = null;
  let inHtmlBlock = false;

  for (const line of markdown.split(/\r?\n/)) {
    if (inHtmlBlock) {
      // An HTML block runs to the next blank line; markdown inside it is text.
      inHtmlBlock = line.trim() !== "";
      continue;
    }
    const open = FENCE_OPEN.exec(line);
    if (fence) {
      // A fence closes on the same marker, at least as long, with nothing after.
      if (open && open[1][0] === fence[0] && open[1].length >= fence.length && line.trim() === open[1]) {
        fence = null;
      }
      continue;
    }
    if (open) {
      fence = open[1];
      continue;
    }
    if (HTML_BLOCK_OPEN.test(line)) {
      inHtmlBlock = true;
      continue;
    }
    const match = HEADING.exec(line);
    if (!match) continue;
    const text = plainText(match[2]);
    headings.push({ depth: match[1].length === 2 ? 2 : 3, text, slug: slug(text) });
  }
  return headings;
}
