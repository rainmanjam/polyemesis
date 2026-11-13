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

/** A slugger for ONE document, with github-slugger's algorithm: the second
 *  heading that slugs to `overview` gets `overview-1`, the third `overview-2`,
 *  and a suffixed result is itself recorded, so headings `a`, `a`, `a-1` give
 *  `a`, `a-1`, `a-1-1` and no two headings of a document share an id.
 *  @returns {(text: string) => string} */
export function uniqueSlugger() {
  /** @type {Map<string, number>} */
  const occurrences = new Map();
  return (text) => {
    const base = slugify(text);
    let result = base;
    while (occurrences.has(result)) {
      // `base` is in the map whenever the loop runs: the first pass is only
      // entered with result === base.
      const n = /** @type {number} */ (occurrences.get(base)) + 1;
      occurrences.set(base, n);
      result = `${base}-${n}`;
    }
    occurrences.set(result, 0);
    return result;
  };
}
