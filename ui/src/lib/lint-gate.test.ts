import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/* THE LINTER HAS TO BE ABLE TO FAIL, or it is documentation.
 *
 * `npm run lint` is `oxlint`, and oxlint exits 0 when everything it found was
 * a warning. So every rule below "error" was advice nobody had to take. The
 * rule that mattered most was exhaustive-deps: it is what catches a polling
 * effect closing over a `programme` that is still null at mount, which is the
 * shape of #606, #612 and #646 -- three separate incidents, each of them
 * reported by this linter, in yellow, under a command whose exit code said
 * everything was fine.
 *
 * A settings file is easy to relax in passing, and the symptom of relaxing it
 * is nothing at all: CI goes on passing. So the level is asserted here, where
 * lowering it is a visible act with a test to answer for rather than a word
 * changed in a config nobody re-reads.
 *
 * NOT A LIST OF EVERY RULE. Only the ones promoted deliberately, with the
 * tree at zero violations and zero suppressions. Adding to it is welcome;
 * quietly taking one off it is what this is here to stop. */

const CONFIG = join(import.meta.dirname, "..", "..", ".oxlintrc.json");

/** The oxlint config is JSONC -- it carries its own reasoning in comments, and
 *  every one of them is a whole line, which is all this needs to handle. A
 *  general JS comment stripper is the wrong tool: lib/strip-js-comments.ts
 *  treats a `//` inside a string as a comment, and this file's comments quote
 *  the word "error". */
function parse(): {
  rules: Record<string, unknown>;
  overrides?: { files: string[]; rules: Record<string, unknown> }[];
} {
  const text = readFileSync(CONFIG, "utf8")
    .split("\n")
    .filter((line) => !/^\s*\/\//.test(line))
    .join("\n");
  return JSON.parse(text);
}

const MUST_FAIL_CI = [
  "react/rules-of-hooks",
  "react-hooks/exhaustive-deps",
  "eslint/no-unused-vars",
];

describe("the lint gate", () => {
  it.each(MUST_FAIL_CI)("keeps %s at error, not warning", (rule) => {
    const level = parse().rules[rule];
    expect(
      Array.isArray(level) ? level[0] : level,
      `${rule} is not an error, so oxlint exits 0 on it and CI cannot fail`,
    ).toBe("error");
  });

  it("does not switch any of them off in an override", () => {
    const raw = parse();
    const relaxed: string[] = [];
    for (const o of raw.overrides ?? []) {
      for (const rule of MUST_FAIL_CI) {
        if (rule in o.rules && o.rules[rule] !== "error") {
          relaxed.push(`${o.files.join(",")}: ${rule}`);
        }
      }
    }
    // The existing override turns off only only-export-components, for
    // vendored shadcn. An override is the quiet way to undo the promotion for
    // most of the tree while the top-level level still reads "error".
    expect(relaxed, "an override lowers a rule that must fail CI").toEqual([]);
  });
});
