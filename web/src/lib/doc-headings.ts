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
const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/;

/** Reduces inline markdown to the text a reader sees. */
function plainText(raw: string): string {
  return raw
    .replace(/[ \t]+#+$/, "") // closing run of #
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1") // links and images
    .replace(/`+([^`]*)`+/g, "$1") // inline code
    .replace(/\*{1,3}([^*]+)\*{1,3}/g, "$1") // * emphasis
    .replace(/(?<!\w)_{1,3}([^_]+)_{1,3}(?!\w)/g, "$1") // _ emphasis, not snake_case
    .trim();
}

/** ATX `##` and `###` headings outside fenced code, in document order. */
export function extractHeadings(markdown: string): DocHeading[] {
  const slug = uniqueSlugger();
  const headings: DocHeading[] = [];
  let fence: string | null = null;

  for (const line of markdown.split(/\r?\n/)) {
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
    const match = HEADING.exec(line);
    if (!match) continue;
    const text = plainText(match[2]);
    headings.push({ depth: match[1].length === 2 ? 2 : 3, text, slug: slug(text) });
  }
  return headings;
}
