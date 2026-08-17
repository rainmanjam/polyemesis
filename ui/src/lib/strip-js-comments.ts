/* The TypeScript half of one function that exists twice.
 *
 * The Go original is StripJSComments in internal/testenv/uisource.go. This is a
 * port, and it cannot be collapsed into that one: vitest cannot call Go.
 *
 * EXTRACTED FROM tour-drift.test.ts SO IT CAN BE MEASURED. While it lived inside
 * a test file nothing could import it, so the only evidence the two copies
 * agreed was a hand comparison done once when the Go version was promoted --
 * recorded in its comment, along with the admission that "nothing enforces
 * that". strip-js-comments.test.ts now drives this against
 * internal/testenv/testdata/js-comment-corpus.json, the same file the Go test
 * reads.
 *
 * NEITHER COPY IS THE ORACLE FOR THE OTHER. The corpus is the spec. Changing the
 * behaviour means changing the corpus, which makes the other language's suite go
 * red in the same commit — which is the only mechanism that survives somebody
 * fixing a bug in one copy and not the other.
 *
 * THE TWO RULES, unchanged from the Go version:
 *
 *  - Block comments (and the {/* *\/} JSX form, which is a block comment inside
 *    an expression container) are removed outright, with their newlines kept so
 *    a line number quoted in a failure still means something.
 *  - Line comments are removed only when nothing before the `//` on that line is
 *    quoted, so a URL inside a string literal is left alone rather than
 *    truncated at its scheme separator.
 *
 * IT IS NOT A PARSER and does not pretend to be: a `/*` inside a string literal
 * reads as a comment opener. That trade is written out in the Go file, and the
 * alternative — a real TypeScript parse — is what these guards exist to avoid
 * depending on.
 */

/** Whether a quote appears between the start of the line containing `i` and `i`
 *  itself — the cheap test for "this `//` is inside a string literal or a JSX
 *  attribute rather than starting a comment". */
function quotedBefore(src: string, i: number): boolean {
  const start = src.lastIndexOf("\n", i - 1) + 1;
  return /["'`]/.test(src.slice(start, i));
}

/** Blanks out comments so a marker left behind in one cannot satisfy a guard
 *  that is asking whether a control is wired. */
export function stripJSComments(src: string): string {
  let out = "";
  let i = 0;
  while (i < src.length) {
    if (src.startsWith("/*", i)) {
      const end = src.indexOf("*/", i + 2);
      if (end < 0) break; // unterminated; the rest is comment
      for (const ch of src.slice(i, end + 2)) if (ch === "\n") out += "\n";
      i = end + 2;
      continue;
    }
    if (src.startsWith("//", i) && !quotedBefore(src, i)) {
      const end = src.indexOf("\n", i);
      if (end < 0) break;
      i = end; // leave the newline for the next iteration
      continue;
    }
    out += src[i];
    i++;
  }
  return out;
}
