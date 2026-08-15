import { defineCollection } from "astro:content";
import { glob } from "astro/loaders";
import { PUBLISHED, BY_FILE } from "./data/docs.mjs";

/* The documentation lives in docs/ at the repo root, versioned with the code,
 * and is loaded from there rather than copied into src/. A copy would guarantee
 * a page that disagrees with the software it documents -- which is the argument
 * the old /docs page made for linking to GitHub instead, and it is still right.
 * What was wrong was the conclusion: the fix is to render the originals, not to
 * send every reader to somebody else's domain.
 *
 * `pattern` IS THE ALLOWLIST, and it is an explicit list of filenames rather
 * than `*.md` with exclusions. src/data/docs.mjs says at length why; the short
 * version is that a glob makes the next internal note public by default, and two
 * of the files in that directory are ones this project would be embarrassed to
 * publish. check-build.mjs asserts that what reached dist/ is exactly this list.
 */
export const collections = {
  docs: defineCollection({
    loader: glob({
      base: "../docs",
      pattern: PUBLISHED.map((d) => d.file),
      /* The id is the URL segment, taken from the manifest rather than derived
         from the filename. The default derivation happens to produce the same
         string today; relying on that would mean a change in Astro's slugifier
         silently moves 23 published URLs. */
      generateId: ({ entry }) => BY_FILE.get(entry)?.slug ?? entry,
    }),
  }),
};
