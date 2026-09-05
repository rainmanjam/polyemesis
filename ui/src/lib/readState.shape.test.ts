import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

/* NO READ IN THIS APP STORES ITS FAILURE AS AN EMPTY RESULT. #719.
 *
 * readState.ts has existed to make `.catch(() => setX([]))` unwriteable since
 * the three cards in SettingsPage and AutomationPage were fixed. What enforced
 * it was a list of FILENAMES with specific source strings asserted absent from
 * each -- and a list of filenames does not know about the file added tomorrow.
 * It did not know DestinationDialog.tsx existed, and DestinationDialog.tsx had
 * five of them.
 *
 * That is training, not a device. This walks every file instead and matches on
 * the SHAPE, so the next one fails on the day it is written.
 *
 * Warning rather than Control: nothing in TypeScript stops a component holding
 * a bare array and assigning [] in a catch. The earliest a device can speak is
 * when the code is written, which is here.
 *
 * WHY THE EMPTY VALUE IS THE PROBLEM AND NOT THE CATCH. A failed read stored as
 * [] or null is indistinguishable from a successful empty one, and an empty
 * state is a POSITIVE CLAIM: it says the server answered and there is nothing
 * there. Every instance found so far went further and drove the operator to
 * act -- regenerate a working client secret, connect an account already
 * connected, build a second encode they already had. */

const ROOT = new URL("../../../", import.meta.url).pathname;
const UI_SRC = join(ROOT, "ui/src");

/* Reads whose empty-on-failure value is NOT a claim, with the reason.
 *
 * KEYED BY FILE AND SETTER, not by file. A page holds several reads and they
 * are not all the same decision -- DestinationDialog had five and two of them
 * drove the operator to act. A file-level excuse would have covered the other
 * three by accident, which is how the previous enforcement missed this file
 * entirely.
 *
 * An entry here is a decision somebody made and can be argued with; an omission
 * is a hazard nobody saw. Note what most of them have in common: `null` is
 * being used as a genuine third state, and the consumer already branches on it.
 * That is the same distinction readState.ts draws, spelled without the type. */
const notAClaim: Record<string, string> = {
  "ui/src/pages/ClipEditor.tsx:setKeyframes":
    "null is the UNKNOWN state and the timeline draws it as unknown rather than as " +
    "'this recording has no keyframes'; failing the page over a probe would be worse",
  "ui/src/pages/ClipEditor.tsx:setPlan":
    "the same catch sets planError from the failure, so the empty plan is never on " +
    "screen without the reason it is empty beside it",
  "ui/src/pages/Dashboard.tsx:setTargets":
    "null is the unknown state and `if (targets === null) return null` hides the " +
    "metadata composer entirely; this was [] until #719, which rendered the composer " +
    "with zero platforms -- a claim that this broadcast has nowhere to push metadata",
  "ui/src/pages/Dashboard.tsx:setSources":
    "the programme badge is drawn only when there is MORE THAN ONE source, so an " +
    "empty list hides a badge rather than asserting that no programmes exist",
  "ui/src/pages/Dashboard.tsx:setFeatureEnabled":
    "null is the unknown state and the recording/meters notice stays silent on it, " +
    "which the catch's own comment says: it must not claim an exposure it could not verify",
  "ui/src/pages/RenditionsPage.tsx:setCaps":
    "null is unknown; the encoder picker falls back to offering every encoder rather " +
    "than claiming this machine supports none, which would hide working hardware",
  "ui/src/pages/RenditionsPage.tsx:setFonts":
    "null is unknown; the overlay editor keeps the font field open rather than claiming " +
    "the data directory contains no fonts",
  "ui/src/pages/SettingsPage.tsx:setSourceCount":
    "explicitly NOT 0 -- claiming zero sources on a failed read would replace the ingest " +
    "form with 'create a source' on an install that has several. Unknown leaves every " +
    "branch on its pre-existing behaviour",
  "ui/src/pages/SettingsPage.tsx:setSystem":
    "null is unknown and the page says the system read failed rather than rendering an " +
    "install with no FFmpeg, which is the widest read on the page and the one most " +
    "likely to fail on a machine that is otherwise fine",
};

/* `.catch(...)` whose body assigns an empty collection: the exact shape.
 *
 * Deliberately narrow. It matches the assignment of a literal [], null or {}
 * inside a catch and nothing else, because a catch that sets an error message,
 * or retries, or assigns a real fallback value, is not this mistake. */
const EMPTY_ON_FAILURE =
  /\.catch\(\s*\([^)]*\)\s*=>\s*(?:\{\s*)?set[A-Za-z0-9_]*\(\s*(?:okRead\(\s*)?(?:\[\s*\]|null|\{\s*\})\s*\)/g;

/* Blanks comment bodies while PRESERVING NEWLINES, so the line numbers in a
 * failure still point at the file. A guard whose report sends the reader to the
 * wrong line costs more than it saves. */
function stripComments(src: string): string {
  const blank = (m: string) => m.replace(/[^\n]/g, " ");
  return src.replace(/\/\*[\s\S]*?\*\//g, blank).replace(/(^|[^:])\/\/[^\n]*/g, (m, p1) => p1 + blank(m.slice(p1.length)));
}

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "node_modules" || name === "components/ui") continue;
      out.push(...walk(p));
      continue;
    }
    if (!/\.(ts|tsx)$/.test(name) || /\.test\.tsx?$/.test(name)) continue;
    out.push(p);
  }
  return out;
}

describe("a read that failed is never stored as an empty result", () => {
  const files = walk(UI_SRC);

  it("finds files to check, so a pass means something", () => {
    // A walker that resolves nothing passes the assertion below and reports a
    // rule it never applied -- the same "a check that does not run looks like
    // one that passed" this whole file is about.
    expect(files.length).toBeGreaterThan(80);
  });

  it("has no `.catch(() => setX([]))` anywhere in ui/src", () => {
    const hits: string[] = [];
    for (const file of files) {
      const rel = file.slice(ROOT.length);
      // COMMENTS STRIPPED FIRST. This file's own explanation quotes the
      // banned shape, and a guard that fires on prose describing the hazard is
      // a guard people delete rather than obey.
      const src = stripComments(readFileSync(file, "utf8"));
      for (const m of src.matchAll(EMPTY_ON_FAILURE)) {
        const setter = /set[A-Za-z0-9_]*/.exec(m[0])?.[0] ?? "";
        if (`${rel}:${setter}` in notAClaim) continue;
        const line = src.slice(0, m.index ?? 0).split("\n").length;
        hits.push(`${rel}:${line} — ${m[0].replace(/\s+/g, " ")}`);
      }
    }
    expect(
      hits,
      "A failed read stored as an empty value is indistinguishable from a successful " +
        "empty one, and every empty state in this app is a positive claim: it says the " +
        "server answered and there is nothing there. Use ReadState from @/lib/readState " +
        "— failedRead() in the catch, mayClaim() before any sentence that asserts what " +
        "came back — or add the file to notAClaim with a reason saying why its empty " +
        "value asserts nothing.\n\n" +
        hits.join("\n"),
    ).toEqual([]);
  });

  it("keeps the excuse list honest", () => {
    for (const [key, why] of Object.entries(notAClaim)) {
      // A reason that says nothing is how a rule becomes something people learn
      // to silence, so the length is checked and placeholders are refused.
      expect(why.length, `${key} is excused with too little to argue with`).toBeGreaterThan(60);
      expect(/TODO|TBD|n\/a/i.test(why), `${key} is excused with a placeholder`).toBe(false);

      // AND THE ENTRY MUST STILL APPLY. An excuse for a read that no longer
      // exists is a standing permission nobody re-earned, sitting there for
      // whatever is written next under the same setter name.
      const [file, setter] = key.split(":");
      const src = stripComments(readFileSync(join(ROOT, file), "utf8"));
      expect(
        new RegExp(`\\.catch\\([^)]*\\)\\s*=>\\s*(?:\\{\\s*)?${setter}\\(`).test(src),
        `${key} is excused but no longer matches the shape — remove the entry`,
      ).toBe(true);
    }
  });

  it("the guard can actually see the shape it bans", () => {
    // A POSITIVE CONTROL. The regex is the whole device, and a regex that
    // silently stopped matching would leave every assertion above passing over
    // nothing. These are the spellings found in the tree before the fix.
    for (const s of [
      "api.listAccounts().then(setAccounts).catch(() => setAccounts([]))",
      ".catch(() => setRenditions([]))",
      ".catch(() => { setCreds(null) })",
      ".catch((e) => setRows([]))",
      // okRead([]) IS THE SAME MISTAKE WEARING THE FIX. Adopting ReadState and
      // then storing a successful-looking empty read in the catch puts the two
      // states back together, and the type no longer objects.
      ".catch(() => setRenditionsRead(okRead([])))",
    ]) {
      expect(new RegExp(EMPTY_ON_FAILURE.source).test(s), `not matched: ${s}`).toBe(true);
    }
    // And what it must NOT match, or it becomes a rule people learn to silence.
    for (const s of [
      '.catch(() => setError("could not load"))',
      ".catch(() => setRead(failedRead()))",
      ".catch(() => setRows(CACHED_DEFAULTS))",
    ]) {
      expect(new RegExp(EMPTY_ON_FAILURE.source).test(s), `wrongly matched: ${s}`).toBe(false);
    }
  });
});
