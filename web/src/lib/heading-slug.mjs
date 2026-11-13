// @ts-check
/* One rule for heading ids, shared by the markdown pipeline and the "On this
 * page" list, so the two can never disagree.
 *
 * The rule is GitHub's, on purpose: docs/*.md were written to be read on
 * GitHub and carry cross-document links such as `AUDIO-ROUTING.md#mix-matrix`.
 * mdast-doc-links.mjs keeps the fragment, so those links only keep working here
 * if a heading's id is what GitHub would have made of it.
 */

/** Lower-case; drop everything that is not a letter, digit, space, hyphen or
 *  underscore; each space becomes a hyphen. Nothing is collapsed or trimmed, so
 *  "Install (Docker) & more" is `install-docker--more`, as on GitHub.
 *  @param {string} text
 *  @returns {string} */
export function slugify(text) {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{N} _-]/gu, "")
    .replace(/ /g, "-");
}

/** A slugger for ONE document. The second heading that slugs to `overview`
 *  gets `overview-1`, the third `overview-2`; a suffixed id is also skipped when
 *  a heading really has it, so no two headings of a document share an id.
 *  @returns {(text: string) => string} */
export function uniqueSlugger() {
  /** @type {Map<string, number>} */
  const repeats = new Map();
  /** @type {Set<string>} */
  const taken = new Set();
  return (text) => {
    const base = slugify(text);
    let n = repeats.get(base) ?? 0;
    let slug = n === 0 ? base : `${base}-${n}`;
    while (n > 0 && taken.has(slug)) {
      n++;
      slug = `${base}-${n}`;
    }
    repeats.set(base, n + 1);
    taken.add(slug);
    return slug;
  };
}
