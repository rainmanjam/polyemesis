import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { EASE_OUT, MOTION } from "./motion";

/* THE CSS BLOCK IS THE SYSTEM; THIS FILE IS ONE CONSUMER.
 *
 * docs/DESIGN-SYSTEM.md is explicit about the mechanism: the `:root` block in
 * index.css is plain CSS with no Tailwind in it, so the website can include the
 * same block and get the same palette, type scale and motion. `@theme inline`
 * below it is the app's Tailwind adapter and is one consumer of that block.
 * lib/motion.ts is now a second consumer, for the JavaScript half.
 *
 * A second consumer that RESTATES the values is a second source of truth
 * waiting to happen, and the symptom of the drift is silent: somebody retunes
 * --motion-settle to 220ms, every CSS transition follows, and the two dialogs
 * animated in JavaScript keep running at 260ms. Nothing errors, nothing looks
 * broken in isolation, and the vocabulary has quietly become two vocabularies.
 *
 * So the restatement is checked rather than trusted. Same shape as
 * preset-drift.test.ts and tour-drift.test.ts: duplicate the value where it has
 * to be duplicated, then make the duplicate diverging a test failure.
 *
 * IT PARSES `:root` SPECIFICALLY, not the whole file. index.css also collapses
 * these tokens to 0ms inside a prefers-reduced-motion block, and a naive
 * whole-file grep would match that override and assert that every duration is
 * zero -- a test that passes for the wrong reason and stops guarding anything. */

const CSS = join(import.meta.dirname, "..", "index.css");

/** The `:root { ... }` block, and only it.
 *
 *  `:root` appears three times in this stylesheet -- the real declaration, the
 *  reduced-motion override, and nothing else today but the file is long. The
 *  first one is the source of truth by construction: it is the block the
 *  website copies, and it is above every media query in the file. */
function rootBlock(): string {
  const text = readFileSync(CSS, "utf8");
  const start = text.indexOf(":root {");
  expect(start, "index.css no longer has a `:root {` block to read tokens from").toBeGreaterThan(-1);
  const end = text.indexOf("\n}", start);
  expect(end, "the `:root` block in index.css is unterminated").toBeGreaterThan(start);
  return text.slice(start, end);
}

/** One `--name: <n>ms;` declaration, in milliseconds. */
function ms(name: string): number {
  const m = new RegExp(`--${name}:\\s*(\\d+(?:\\.\\d+)?)ms`).exec(rootBlock());
  expect(m, `--${name} is not declared in the :root block of index.css`).not.toBeNull();
  return Number(m![1]);
}

describe("the motion vocabulary", () => {
  it.each([
    ["motion-instant", MOTION.instant],
    ["motion-quick", MOTION.quick],
    ["motion-settle", MOTION.settle],
  ])("keeps %s in step with lib/motion.ts", (token, seconds) => {
    expect(
      seconds * 1000,
      `--${token} in index.css and MOTION in lib/motion.ts disagree, so CSS transitions and JS animations now run at different speeds`,
    ).toBe(ms(token));
  });

  it("keeps --ease-out in step with EASE_OUT", () => {
    // Same curve, two notations: CSS spells it cubic-bezier(a, b, c, d) and
    // motion takes the four control points as an array. A mismatch here is the
    // subtler half of the same drift -- identical durations, different feel.
    const m = /--ease-out:\s*cubic-bezier\(([^)]+)\)/.exec(rootBlock());
    expect(m, "--ease-out is not declared in the :root block of index.css").not.toBeNull();
    const points = m![1].split(",").map((n) => Number(n.trim()));
    expect(points).toEqual([...EASE_OUT]);
  });

  it("declares exactly three durations, because a fourth gets chosen by accident", () => {
    // The scale is capped on purpose, the way the type scale is capped at six
    // steps and the signal palette at four hues. A --motion-enter added beside
    // these would not be a new token, it would be a claim that a fourth kind of
    // state change exists, and that claim belongs in DESIGN-SYSTEM.md before it
    // belongs in the stylesheet.
    const declared = [...rootBlock().matchAll(/--motion-([a-z-]+):/g)].map((m) => m[1]);
    expect(declared.sort()).toEqual(["instant", "quick", "settle"]);
    expect(Object.keys(MOTION).sort()).toEqual(["instant", "quick", "settle"]);
  });

  it("still collapses all three to zero under prefers-reduced-motion", () => {
    // The override the whole reduced-motion path depends on. It has been
    // decorative in this file once before -- the durations were declared and
    // registered nowhere, so setting them to 0ms changed nothing -- and this is
    // what would notice it being deleted as dead code.
    const text = readFileSync(CSS, "utf8");
    const block = text.slice(text.indexOf("@media (prefers-reduced-motion: reduce)"));
    for (const token of ["motion-instant", "motion-quick", "motion-settle"]) {
      expect(
        new RegExp(`--${token}:\\s*0ms`).test(block),
        `--${token} is no longer collapsed to 0ms for operators who asked for less motion`,
      ).toBe(true);
    }
  });
});
