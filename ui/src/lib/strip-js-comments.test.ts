import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { stripJSComments } from "./strip-js-comments";

/* THE OTHER HALF OF A CROSS-LANGUAGE CHECK.
 *
 * internal/testenv/js_comment_corpus_test.go drives the Go implementation
 * against this same file. Neither implementation is the oracle: the corpus is
 * the spec, and the only way to change the behaviour is to change the corpus —
 * which makes the OTHER language's suite go red in the same commit.
 *
 * That mechanism is the point. The Go comment recorded that the two copies were
 * compared by hand once, over all 96 .ts and .tsx files, byte-identical — and
 * that nothing enforced it afterwards. A hand comparison is evidence about one
 * afternoon; it cannot survive somebody fixing a bug in one copy, which is the
 * only way this function ever changes.
 *
 * The two copies also guard DIFFERENT things — Go the Facebook settings and
 * capability mirrors, TypeScript the tour's anchors — so a divergence would show
 * up as one suite going quietly permissive rather than as a conflict anybody
 * notices.
 *
 * READ FROM THE GO TREE ON PURPOSE. A copy of the corpus under ui/ would be a
 * third thing to keep in step, and the first one to drift.
 */

const CORPUS = new URL(
  "../../../internal/testenv/testdata/js-comment-corpus.json",
  import.meta.url,
).pathname;

type Case = { name: string; input: string; want: string };

function corpus(): Case[] {
  const cases = JSON.parse(readFileSync(CORPUS, "utf8")) as Case[];
  if (!Array.isArray(cases) || cases.length === 0) {
    throw new Error(
      `the shared corpus at ${CORPUS} is empty or unreadable, so this suite is ` +
        `pinned to nothing`,
    );
  }
  return cases;
}

describe("stripJSComments agrees with the shared corpus", () => {
  for (const tc of corpus()) {
    it(tc.name, () => {
      // The message matters more than usual here: whoever sees this is being
      // told that two implementations in two languages have diverged, which is
      // not the first thing anybody suspects.
      expect(
        stripJSComments(tc.input),
        `stripJSComments disagrees with internal/testenv/testdata/js-comment-corpus.json.\n` +
          `That file is the spec for BOTH this and StripJSComments in ` +
          `internal/testenv/uisource.go. If the change is intended, update the ` +
          `corpus — the Go suite will then fail until it is updated too, which is ` +
          `the mechanism.`,
      ).toBe(tc.want);
    });
  }
});
