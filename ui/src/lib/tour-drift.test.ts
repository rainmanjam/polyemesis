/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

import en from "./i18n/en.json";
import { TOUR_STEPS } from "./tourSteps";

/* ===========================================================================
   The onboarding tour's selectors still match something
   ===========================================================================

   A TOUR FAILS SILENTLY. A selector that stops matching does not throw, does
   not warn and does not log: driver.js dims the screen, cuts a hole out of
   nothing and moves on. Nobody notices until a user says the highlight lands in
   the wrong place, months later, and by then the commit that renamed the
   attribute is long merged. Every other kind of breakage in this app announces
   itself; this one does not, which is the whole reason for this file.

   Modelled on lib/theme.test.ts (read the source from disk, assert a property
   no reviewer can check by reading) and on internal/db/facebook_ui_drift_test.go
   (strip comments first -- see below).

   ===== THE COMMENT-DEFEAT TRAP =====

   A guard that greps a .tsx for a string passes forever if the string survives
   only in a comment. That is not hypothetical: it is the honest way to keep a
   substring guard green while deleting the thing it watches, and
   facebook_ui_drift_test.go ships stripJSComments for exactly this reason.
   `stripJSComments` below is that function ported, and
   `TestTheGuardCannotBeSatisfiedByAComment` -- "a comment satisfies nothing"
   here -- is its positive control: it plants an anchor in each comment form and
   requires the guard to still call it missing.

   ===== WHAT THIS CHECKS, AND WHAT IT CANNOT =====

   It checks that the anchor is WIRED IN THE SOURCE of the component the step
   names. It cannot check that the element RENDERS -- that would need a DOM, and
   this suite has no jsdom and no React renderer, which is a deliberate
   dependency choice rather than an oversight.

   So the runtime half is carried elsewhere and on purpose: `presence` on each
   step is the claim about whether the element is in the DOM on an empty
   install, driver.js's `skipMissingElement` is what makes a false claim
   survivable, and the presence invariants at the bottom of this file are what
   stop the tour from depending on a step that is allowed to disappear.
   =========================================================================== */

const SRC = new URL("../", import.meta.url).pathname;

/** Blanks out comments so a marker left behind in one cannot satisfy a guard
 *  that is asking whether a control is wired.
 *
 *  Ported from stripJSComments in internal/db/facebook_ui_drift_test.go, with
 *  the same two rules and for the same reason:
 *
 *   - Block comments (`/* *\/`, and the `{/* *\/}` JSX form, which is a block
 *     comment inside an expression container) are removed outright.
 *   - Line comments are removed only when nothing before the `//` on that line
 *     is quoted, so a URL inside a string literal is left alone rather than
 *     truncated at the scheme separator.
 *
 *  Newlines are preserved so a line number quoted in a failure still means
 *  something to whoever goes to look. */
function stripJSComments(src: string): string {
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

/** Whether a quote appears between the start of the line containing `i` and `i`
 *  itself — the cheap test for "this `//` is inside a string literal or a JSX
 *  attribute rather than starting a comment". */
function quotedBefore(src: string, i: number): boolean {
  const start = src.lastIndexOf("\n", i - 1) + 1;
  return /["'`]/.test(src.slice(start, i));
}

/** The only selector shape a step may use.
 *
 *  Enforced rather than assumed. If a step could carry an arbitrary selector —
 *  `.card > button:first-child` — this guard would have nothing mechanical to
 *  go looking for in the owner file, and would have to either parse CSS or
 *  quietly stop checking. Pinning the shape is what keeps the check total. */
const ANCHOR_RE = /^\[data-tour="([a-z][a-z0-9-]*)"\]$/;

function anchorName(selector: string): string {
  const m = ANCHOR_RE.exec(selector);
  if (!m) throw new Error(`not an anchor selector: ${selector}`);
  return m[1];
}

function tsxFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry: string) => {
    const p = join(dir, entry);
    return statSync(p).isDirectory() ? tsxFiles(p) : p.endsWith(".tsx") ? [p] : [];
  });
}

/** Whether `src` wires `name` as a tour anchor, reading only non-comment text.
 *
 *  TWO CONDITIONS, BOTH REQUIRED, and the pair is what covers both shapes the
 *  anchors take in this codebase:
 *
 *   - a LITERAL attribute — `data-tour="add-destination"` on the element
 *     itself, which is how the page components spell it. This satisfies both
 *     conditions at once.
 *   - a TABLE-DRIVEN attribute — AppLayout maps over NAV and renders
 *     `data-tour={tour}`, so the value lives in a data row rather than in the
 *     JSX. There is no attribute literal to find, and requiring one would mean
 *     either failing a file that is entirely correct or writing the value out
 *     twice.
 *
 *  Requiring the binding AND the value covers the realistic drift in both
 *  shapes: renaming the anchor on one side only fails the value check; deleting
 *  the binding fails the attribute check; deleting the whole thing fails both.
 *
 *  WHAT IT DOES NOT PROVE, stated rather than glossed: in the table-driven
 *  shape the two conditions are checked independently, so a file that still
 *  contains the string `"nav-sources"` for some unrelated reason AND still has
 *  a `data-tour` binding somewhere would pass while the pairing between them
 *  was broken. Closing that needs a renderer. The presence invariants below and
 *  driver.js's skip path are what make the residue survivable. */
function wiresAnchor(src: string, name: string): { attribute: boolean; value: boolean } {
  const stripped = stripJSComments(src);
  return {
    attribute: /\bdata-tour\s*=/.test(stripped),
    // A whole double-quoted literal, so `nav-source` cannot be satisfied by
    // `nav-sources`.
    value: stripped.includes(`"${name}"`),
  };
}

function ownerSource(owner: string): string {
  return readFileSync(join(SRC, owner), "utf8");
}

describe("the tour's anchors are wired in the components it names", () => {
  it("every step uses the one selector shape this guard can check", () => {
    const wrong = TOUR_STEPS.filter((s) => !ANCHOR_RE.test(s.element)).map(
      (s) => `${s.id}: ${s.element}`,
    );
    expect(
      wrong,
      'a step must anchor on [data-tour="…"]. An arbitrary CSS selector cannot be ' +
        "checked against the source, so this guard would silently stop covering it.",
    ).toEqual([]);
  });

  it("every step names a file that exists", () => {
    const missing = TOUR_STEPS.filter((s) => {
      try {
        statSync(join(SRC, s.owner));
        return false;
      } catch {
        return true;
      }
    }).map((s) => `${s.id} -> ${s.owner}`);
    expect(
      missing,
      "a step names an owner file that is not there. Every assertion below reads " +
        "that file, so a wrong path is a guard that checks nothing.",
    ).toEqual([]);
  });

  it.each(TOUR_STEPS.map((s) => [s.id, s] as const))(
    "%s: its anchor is wired in its owner, outside any comment",
    (_id, step) => {
      const name = anchorName(step.element);
      const { attribute, value } = wiresAnchor(ownerSource(step.owner), name);

      expect(
        attribute,
        `${step.owner} renders no data-tour attribute at all, so the tour step ` +
          `"${step.id}" highlights nothing. driver.js does not warn about this: it dims ` +
          `the page and cuts a hole out of the background.`,
      ).toBe(true);

      expect(
        value,
        `${step.owner} does not carry the anchor name "${name}" outside a comment. ` +
          `The tour step "${step.id}" selects ${step.element}, so either the attribute ` +
          `was renamed on one side only, or the value survives only in a comment — ` +
          `which is the exact way a substring guard is kept green while the thing it ` +
          `watches is deleted.`,
      ).toBe(true);
    },
  );

  /* GUARDING THE GUARD. Every assertion above is of the form "this string is in
     that file", and every one of them would pass vacuously if stripJSComments
     ever stopped removing comments — the anchors would still be found, just in
     the wrong kind of text.

     So the comment-stripper is driven directly, against each comment form this
     codebase actually uses, with an anchor planted inside it. If any of these
     survive stripping, the guard has been defeated and the whole file above is
     decoration. */
  it("a comment satisfies nothing", () => {
    const forms: [string, string][] = [
      ["line", `const x = 1;\n// data-tour="add-destination"\n`],
      ["block", `const x = 1;\n/* data-tour="add-destination" */\n`],
      ["jsx", `<div>\n  {/* data-tour="add-destination" */}\n</div>\n`],
      [
        "block spanning lines",
        `const x = 1;\n/*\n  data-tour="add-destination"\n*/\nconst y = 2;\n`,
      ],
    ];
    for (const [label, src] of forms) {
      const { attribute, value } = wiresAnchor(src, "add-destination");
      expect(attribute, `a ${label} comment was read as a rendered attribute`).toBe(false);
      expect(value, `a ${label} comment was read as a wired anchor name`).toBe(false);
    }

    // And the stripper must not be so eager that it eats real code: a `//`
    // inside a string is a URL, not a comment, and blanking the rest of that
    // line would make every assertion above fail for the wrong reason.
    const withURL = `const u = "rtmp://ingest.example/live"; const a = "add-destination";`;
    expect(stripJSComments(withURL)).toContain("add-destination");
    expect(stripJSComments(withURL)).toContain("rtmp://ingest.example/live");
  });

  /* THE OTHER DIRECTION. An anchor in a component that no step names is dead
     weight: somebody wired it, the step that used it was removed or renamed,
     and the attribute stayed behind looking load-bearing. The next person to
     refactor that component has no way to tell it from a live one. */
  it("no component carries a data-tour anchor the tour does not use", () => {
    const named = new Set(TOUR_STEPS.map((s) => anchorName(s.element)));
    const orphans: string[] = [];
    for (const file of tsxFiles(SRC)) {
      const stripped = stripJSComments(readFileSync(file, "utf8"));
      for (const m of stripped.matchAll(/data-tour="([^"]+)"/g)) {
        if (!named.has(m[1])) orphans.push(`${file.slice(SRC.length)}: ${m[1]}`);
      }
    }
    expect(
      orphans,
      "these data-tour attributes are named by no tour step. Delete them, or add the " +
        "step that was meant to use them — an anchor nobody reads is an invitation to " +
        "keep maintaining it.",
    ).toEqual([]);
  });
});

describe("the tour's copy and routes exist", () => {
  const english = en as Record<string, string>;

  it.each(TOUR_STEPS.map((s) => [s.id, s] as const))(
    "%s: its title and body are defined in en.json",
    (_id, step) => {
      for (const key of [step.titleKey, step.bodyKey]) {
        expect(english[key], `en.json has no ${key}`).toBeDefined();
        expect(
          (english[key] ?? "").trim(),
          `en.json ${key} is empty, so this step's popover renders a blank`,
        ).not.toBe("");
      }
    },
  );

  /* The chrome keys the popover itself needs. These are not attached to any
     step, so nothing above would notice them going missing — and a tour whose
     Next button has no label is a tour nobody can finish. */
  it("the popover chrome is translated too", () => {
    for (const key of [
      "tour.next",
      "tour.previous",
      "tour.done",
      "tour.progress",
      "tour.replay",
      "tour.offerTitle",
      "tour.offerBody",
      "tour.offerStart",
      "tour.offerDismissAria",
    ]) {
      expect(english[key], `en.json has no ${key}`).toBeDefined();
      expect((english[key] ?? "").trim(), `en.json ${key} is empty`).not.toBe("");
    }
  });

  /* driver.js substitutes {{current}} and {{total}} itself. lib/i18n.ts's own
     substitution is `{name}` and only runs when params are passed — the tour
     passes none — so the doubled braces reach driver.js intact. If a translator
     "fixed" them to single braces the progress text would render literally. */
  it("the progress text keeps driver.js's own placeholders", () => {
    expect(english["tour.progress"]).toContain("{{current}}");
    expect(english["tour.progress"]).toContain("{{total}}");
  });

  /* A step navigates to its route before looking for its anchor. A route the
     router does not serve lands on the catch-all redirect to "/", so the step
     would look for its element on the dashboard and silently skip. */
  it("every route a step navigates to is served by the router", () => {
    const app = stripJSComments(readFileSync(join(SRC, "App.tsx"), "utf8"));
    const served = new Set(
      [...app.matchAll(/<Route\s+path="([^"]+)"/g)].map((m) => m[1]),
    );
    const unserved = TOUR_STEPS.filter((s) => !served.has(s.route.split("?")[0])).map(
      (s) => `${s.id} -> ${s.route}`,
    );
    expect(
      unserved,
      "a step navigates somewhere App.tsx does not route. The catch-all sends that to " +
        `"/", so the step looks for its anchor on the dashboard and is skipped without ` +
        "a word.",
    ).toEqual([]);
  });
});

/* ============================ the empty install ============================

   Nothing is seeded on a fresh install — no sources, no destinations, no
   renditions — so "the anchor exists" cannot lean on any row existing. These
   are the invariants that keep the tour usable in that state, and they are here
   rather than in a comment because the arrangement they describe is exactly the
   kind that decays: somebody adds a seventh step, marks it whenConfigured
   because it was easier, and the tour quietly becomes three steps on a first
   run.
   =========================================================================== */
describe("the tour survives an install where nothing is configured", () => {
  it("step ids are unique", () => {
    const seen = new Set<string>();
    const dupes = TOUR_STEPS.filter((s) => (seen.has(s.id) ? true : (seen.add(s.id), false)));
    expect(dupes.map((s) => s.id), "two steps share an id").toEqual([]);
  });

  it("the first step is always present", () => {
    // A tour whose opening step can be skipped starts on step two with no
    // explanation of what is happening, which reads as a bug rather than as a
    // tour.
    expect(
      TOUR_STEPS[0]?.presence,
      "the first step must render on an empty install, or the tour opens mid-thought",
    ).toBe("always");
  });

  it("most steps render with nothing configured", () => {
    const always = TOUR_STEPS.filter((s) => s.presence === "always");
    // Not a count: a proportion. The point is that a first-run operator gets a
    // tour rather than a handful of steps that happened to survive.
    expect(
      always.length * 2,
      `only ${always.length} of ${TOUR_STEPS.length} steps are guaranteed to render on ` +
        "an empty install. That is the install this feature exists for, so a tour that " +
        "mostly evaporates there is not a tour.",
    ).toBeGreaterThan(TOUR_STEPS.length);
  });

  it("skipping a conditional step cannot strand the tour on the wrong page", () => {
    /* Tour.tsx navigates to a step's route BEFORE driver.js decides whether to
       skip it, because the element cannot be looked for on a page that is not
       mounted. So when a conditional step is dropped, the router is left on
       that step's route while driver.js moves on to the one after — and if the
       two routes differ, the next step looks for its anchor on the wrong page.

       On an empty install the skip happens EVERY time, so this is the ordinary
       path rather than a corner. Requiring the conditional step to share a
       route with its successor is what makes the drop invisible. */
    const stranding = TOUR_STEPS.filter(
      (s, i) =>
        s.presence === "whenConfigured" &&
        TOUR_STEPS[i + 1] !== undefined &&
        TOUR_STEPS[i + 1].route !== s.route,
    ).map((s) => `${s.id} (${s.route}) -> ${TOUR_STEPS[TOUR_STEPS.indexOf(s) + 1].id}`);
    expect(
      stranding,
      "a step that may be skipped does not share its route with the step after it. The " +
        "tour navigates before it knows whether it is skipping, so dropping this one " +
        "leaves the router where the next step's anchor is not — and on an empty " +
        "install that happens on every run.",
    ).toEqual([]);
  });

  it("every conditional step is preceded by an unconditional one", () => {
    // THE LOAD-BEARING RULE. A conditional step may be dropped, so whatever it
    // teaches has to have a home that cannot be dropped. In this tour that is
    // literal: `add-source` carries #384's first point precisely so that
    // `publish-url` disappearing on a fresh install costs a demonstration and
    // not the lesson.
    const orphaned = TOUR_STEPS.filter(
      (s, i) => s.presence === "whenConfigured" && TOUR_STEPS[i - 1]?.presence !== "always",
    ).map((s) => s.id);
    expect(
      orphaned,
      "a step that may be skipped is not backed by a step that cannot be. Whatever it " +
        "teaches has to survive its absence, or a first-run operator — who has nothing " +
        "configured, and is the whole audience for this — is taught nothing.",
    ).toEqual([]);
  });
});
