import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

/* Every platform a destination can carry has a proper name on its card.
 *
 * DestinationCard's PLATFORM_LABEL falls back to the raw platform word, so a
 * missing entry is not a crash: it is a bare lowercase "x" or "facebook" beside
 * capitalised siblings, which is how Facebook's gap went unnoticed. Read as text
 * because the map is private to the component. */

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), "utf8");

function platformUnion(): string[] {
  const types = read("../lib/types.ts");
  const m = /export type Platform =([^;]+);/.exec(types);
  if (!m) throw new Error("Platform union not found in lib/types.ts");
  return [...m[1].matchAll(/"([a-z]+)"/g)].map((x) => x[1]);
}

function labelKeys(file: string): Set<string> {
  const src = read(file);
  const m = /const PLATFORM_LABEL: Record<string, string> = \{([\s\S]*?)\n\};/.exec(src);
  if (!m) throw new Error(`PLATFORM_LABEL not found in ${file}`);
  return new Set([...m[1].matchAll(/^\s*([a-z]+):/gm)].map((x) => x[1]));
}

describe("platform labels", () => {
  it("names every destination platform on the destination card", () => {
    const keys = labelKeys("./DestinationCard.tsx");
    // "custom" is the one platform with no name of its own: the card shows the kind.
    const missing = platformUnion().filter((p) => p !== "custom" && !keys.has(p));
    expect(missing).toEqual([]);
  });
});
