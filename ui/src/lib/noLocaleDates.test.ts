import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

/* Every date or time the console draws goes through lib/format, which renders
 * it in the install's display zone. A toLocale*String call on a Date anywhere
 * else draws in the browser's zone instead, and two people on one production
 * stop being able to read each other's screenshots.
 *
 * toLocaleString on a NUMBER (1,234) is fine and everywhere, so that method is
 * only flagged when its receiver is visibly a Date: `new Date(...)` itself, or
 * a name this file assigns from `new Date(...)`. toLocaleDateString and
 * toLocaleTimeString only exist on dates and are flagged outright.
 *
 * PublicPlayer is exempt: it is the viewer-facing page, drawn in the viewer's
 * own zone and language on purpose, with no session and no install settings. */

const SRC = join(__dirname, "..");
const EXEMPT = new Set(["lib/format.ts", "pages/PublicPlayer.tsx"]);

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) sources(p, out);
    else if (/\.(ts|tsx)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name)) out.push(p);
  }
  return out;
}

function dateLocaleCalls(text: string): string[] {
  const hits: string[] = [];
  for (const m of text.matchAll(/\.toLocale(Date|Time)String\(/g)) hits.push(m[0]);
  for (const m of text.matchAll(/new Date\((?:[^()]|\([^()]*\))*\)\.toLocaleString\(/g)) hits.push(m[0]);
  for (const m of text.matchAll(/\b(\w+)\s*=\s*new Date\(/g)) {
    if (new RegExp(`\\b${m[1]}\\.toLocaleString\\(`).test(text)) hits.push(`${m[1]}.toLocaleString(`);
  }
  return hits;
}

describe("dates are drawn through lib/format", () => {
  it("has no toLocale*String on a Date outside it", () => {
    const offenders: string[] = [];
    for (const file of sources(SRC)) {
      const rel = relative(SRC, file).split("\\").join("/");
      if (EXEMPT.has(rel)) continue;
      const hits = dateLocaleCalls(readFileSync(file, "utf8"));
      if (hits.length) offenders.push(`${rel}: ${hits.join(", ")}`);
    }
    expect(offenders, "use timestamp / clockTime / clockMinutes / dateOnly from @/lib/format").toEqual([]);
  });

  it("catches what it is meant to catch", () => {
    expect(dateLocaleCalls("x.toLocaleTimeString()").length).toBe(1);
    expect(dateLocaleCalls("d.toLocaleDateString('en')").length).toBe(1);
    expect(dateLocaleCalls("new Date(a.at).toLocaleString()").length).toBe(1);
    expect(dateLocaleCalls("const used = new Date(x);\nused.toLocaleString()").length).toBe(1);
    expect(dateLocaleCalls("count.toLocaleString()")).toEqual([]);
  });
});
