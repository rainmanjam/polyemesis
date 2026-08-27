// @vitest-environment node

/* A HOOK THAT USES `programme` MUST DECLARE IT.
 *
 * Threading the resolved programme into the scoped clients (#606) meant three
 * pages started passing it to calls inside a useEffect or useCallback whose
 * dependency array predated the argument. The arrays were correct when the
 * calls took none. The moment one was added they froze it at the mount value --
 * `null` -- so every poll went out unscoped and took a 400 on any install with
 * two programmes:
 *
 *   MetersPage      the loudness poll, which made the page show NOT UPDATING
 *                   beside a perfectly healthy analyser and hid #612 for hours
 *   MonitoringPage  the process poll, so that page was simply dead
 *   ClipsPage       the clip listing
 *
 * `react-hooks/exhaustive-deps` did not catch any of them: two carried an
 * explicit eslint-disable and the third is a useCallback whose array the rule
 * scores differently. A suppression written for a true statement stays behind
 * when the statement stops being true, which is the whole hazard.
 *
 * So this reads the source. Crude, and it is the only thing that has actually
 * caught this: every hook body mentioning `programme` must name it in its
 * dependency array.
 */

import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const ROOT = new URL("../", import.meta.url).pathname;

/** Every .tsx under src/, so a new page is covered the day it is written. */
function sources(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (e.name.endsWith(".tsx") && !e.name.includes(".test.")) out.push(p);
  }
  return out;
}

describe("every hook that reads the programme declares it", () => {
  it("no useEffect or useCallback closes over a stale programme", () => {
    const offenders: string[] = [];

    for (const file of sources(ROOT)) {
      const src = readFileSync(file, "utf8");
      if (!src.includes("programme")) continue;

      // Each hook call: from `useEffect(`/`useCallback(` to the `}, [...]);`
      // that closes it. Non-greedy, so nested hooks are matched separately.
      const hooks = src.matchAll(
        /use(?:Effect|Callback)\(\s*(?:\([^)]*\)|function[^{]*)\s*=>?\s*\{([\s\S]*?)\n\s*\},\s*\[([^\]]*)\]\s*\)/g,
      );
      for (const m of hooks) {
        const [, body, deps] = m;
        // `programme` used in the body, as a value rather than in prose.
        if (!/\bprogramme\b/.test(body.replace(/\/\/[^\n]*|\/\*[\s\S]*?\*\//g, ""))) continue;
        if (/\bprogramme\b/.test(deps)) continue;
        offenders.push(`${file.replace(ROOT, "")}: uses programme, deps are [${deps.trim()}]`);
      }
    }

    expect(
      offenders,
      "a hook reads the resolved programme but does not list it, so it will hold " +
        "the mount value -- null -- and every scoped call it makes will be refused " +
        "with 400 source_required on an install with two programmes",
    ).toEqual([]);
  });
});
