/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { ALWAYS_ON_ACTION, armedCount, isOperable } from "./automodArmed";
import type { AutomodAction, AutomodChecker } from "./types";

const ACTIONS: AutomodAction[] = [
  "flag",
  "hide_local",
  "hide",
  "delete",
  "timeout",
  "ban",
];
const CHECKERS: AutomodChecker[] = ["rules", "history", "model"];

const all = () => true;

describe("armedCount", () => {
  it("counts a cell the operator has just ticked, before any refetch", () => {
    // THE DEFECT: the collapsed line read `view.summary[platform]`, the
    // server's count from one fetch at mount, while the cells and the
    // irreversible banner read the draft. Arming a ban left the line saying
    // "nothing automatic" directly under "An irreversible action is armed".
    const on = { "twitch/ban/rules": true };
    expect(
      armedCount({
        platform: "twitch",
        actions: ACTIONS,
        checkers: CHECKERS,
        on,
        available: all,
      }),
    ).toBe(1);
  });

  it("drops back to zero the moment the operator disarms, which is the raid case", () => {
    // Mid-raid the same staleness runs the other way: everything is switched
    // off and the line keeps reading "5 automatic actions", so nobody can tell
    // whether the kill switch took.
    expect(
      armedCount({
        platform: "twitch",
        actions: ACTIONS,
        checkers: CHECKERS,
        on: {},
        available: all,
      }),
    ).toBe(0);
  });

  it("never counts flagging, matching Matrix.Summary", () => {
    // automod/matrix.go:220 skips ActionFlag: it changes nothing an audience
    // sees, so it is not "an automatic action" in the sense the operator is
    // asking about. Counting it would report a fresh install — where every
    // flag cell is on by default — as twelve armed actions.
    const on: Record<string, boolean> = {};
    for (const c of CHECKERS) on[`twitch/flag/${c}`] = true;
    expect(
      armedCount({
        platform: "twitch",
        actions: ACTIONS,
        checkers: CHECKERS,
        on,
        available: all,
      }),
    ).toBe(0);
  });

  it("does not count a stored cell the platform can no longer perform", () => {
    // Same order as Matrix.Allows: the capability gate is consulted last and
    // is not overridable. A setting left behind by a capability that has gone
    // away is not something that will happen.
    expect(
      armedCount({
        platform: "facebook",
        actions: ACTIONS,
        checkers: CHECKERS,
        on: { "facebook/ban/rules": true },
        available: () => false,
      }),
    ).toBe(0);
  });

  it("counts only the platform asked about", () => {
    expect(
      armedCount({
        platform: "twitch",
        actions: ACTIONS,
        checkers: CHECKERS,
        on: { "kick/ban/rules": true },
        available: all,
      }),
    ).toBe(0);
  });
});

describe("isOperable", () => {
  it("marks flagging as the one row with nothing to switch", () => {
    expect(isOperable(ALWAYS_ON_ACTION)).toBe(false);
    expect(isOperable("flag")).toBe(false);
    for (const a of ACTIONS.filter((a) => a !== "flag")) {
      expect(isOperable(a), `${a} is a real choice and must stay a switch`).toBe(true);
    }
  });
});

/* AND THAT THE CARD ACTUALLY ASKS. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const src = () => readFileSync(join(ROOT, "ui/src/components/AutomodMatrix.tsx"), "utf8");

describe("AutomodMatrix", () => {
  it("no longer reads the collapsed line off the once-fetched server summary", () => {
    expect(src()).not.toContain("const armed = view.summary[platform] ?? 0;");
    expect(src()).toContain("const armed = armedCount({");
  });

  it("renders the flag row as fixed text rather than twelve inert switches", () => {
    // Findings are recorded before the matrix is consulted and the flag job
    // returns immediately, so each of these switches had one possible outcome.
    expect(src()).toContain("isOperable(action)");
    expect(src()).toContain("recorded for review, never acted on");
  });
});
