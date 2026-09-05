// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";

/* EVERY MERMAID FENCE IN docs/ HAS TO PARSE, AND UNTIL NOW NOTHING CHECKED.
 *
 * mermaid-render.test.ts says so in as many words -- "mermaid throws on a
 * syntax error in a way that is otherwise invisible outside devtools; nothing
 * in the build parses these fences" -- and mitigates it by leaving the source
 * on the page when rendering fails. That mitigation is right and it is not a
 * check: a broken diagram still ships, and the first person to know is a reader
 * looking at a code block where a picture should be.
 *
 * This is the check. It reads the fences out of the markdown the site renders
 * and hands each one to the real parser, which is the same parser the browser
 * will use.
 *
 * WHY jsdom RATHER THAN A BARE NODE PARSE: mermaid initialises DOMPurify at
 * import time and fails with "DOMPurify.addHook is not a function" without a
 * document. That is an environment problem, not a diagram problem, and it is
 * exactly the sort of thing that makes people give up on testing these. */

/** The repository's docs/ directory, found by walking up from wherever the test
 *  runner happens to have started. A path relative to import.meta.url resolves
 *  differently under vitest than under node, which is a fragile thing to hang a
 *  guard on -- and a guard that cannot find its inputs reports a pass. */
function docsDir(): string {
  let dir = process.cwd();
  for (let i = 0; i < 8; i++) {
    const candidate = join(dir, "docs");
    if (existsSync(join(candidate, "TLS.md"))) return candidate;
    const parent = dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error("could not find docs/ above " + process.cwd());
}

const DOCS = docsDir();

/** Every ```mermaid fence in the repository's documentation, with where it came
 *  from, so a failure names the file rather than the index. */
function mermaidBlocks(): { file: string; index: number; source: string }[] {
  const out: { file: string; index: number; source: string }[] = [];
  for (const name of readdirSync(DOCS).filter((f) => f.endsWith(".md"))) {
    const text = readFileSync(join(DOCS, name), "utf8");
    const re = /```mermaid\n([\s\S]*?)```/g;
    let m: RegExpExecArray | null;
    let i = 0;
    while ((m = re.exec(text)) !== null) {
      out.push({ file: name, index: i++, source: m[1] });
    }
  }
  return out;
}

describe("the mermaid diagrams in docs/", () => {
  const blocks = mermaidBlocks();

  it("finds the fences at all", () => {
    // A walker that resolves nothing passes every assertion below and reports a
    // check it never ran -- the same silence this file exists to break.
    expect(blocks.length).toBeGreaterThan(0);
  });

  it.each(blocks.map((b) => [`${b.file} #${b.index}`, b.source] as const))(
    "%s parses",
    async (_where, source) => {
      const mermaid = (await import("mermaid")).default;
      // parse() throws on a syntax error and resolves otherwise. It does not
      // lay the diagram out, which is what keeps this fast and what keeps it
      // testing the thing that actually breaks.
      await expect(mermaid.parse(source)).resolves.toBeTruthy();
    },
  );

  it("rejects a diagram that does not parse, so the check above is not vacuous", async () => {
    const mermaid = (await import("mermaid")).default;
    await expect(mermaid.parse("flowchart TD\n  a --> --> b")).rejects.toBeDefined();
  });
});
