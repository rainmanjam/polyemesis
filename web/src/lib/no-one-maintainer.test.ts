import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const PHRASE = /one[- ]maintainer/i;
const ROOT = new URL("../../../", import.meta.url).pathname;

function filesUnder(dir: string): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...filesUnder(p));
    else if (!/\.test\.[a-z]+$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("retired phrase", () => {
  it('no "one maintainer" anywhere public', () => {
    const docs = readdirSync(join(ROOT, "docs"))
      .filter((f) => f.endsWith(".md"))
      .map((f) => join(ROOT, "docs", f));
    const files = [...filesUnder(join(ROOT, "web/src")), ...docs, join(ROOT, "README.md")];
    expect(files.length).toBeGreaterThanOrEqual(100);
    const hits: string[] = [];
    for (const f of files) {
      let text: string;
      try {
        text = readFileSync(f, "utf8");
      } catch {
        continue;
      }
      text.split("\n").forEach((line, i) => {
        if (PHRASE.test(line)) hits.push(`${f.replace(ROOT, "")}:${i + 1}`);
      });
    }
    expect(hits).toEqual([]);
  });
});
