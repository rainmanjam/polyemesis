/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { keyframeVerdict, windowOnBufferToggle } from "./clipBufferFacts";

describe("keyframeVerdict", () => {
  it("says nothing, in no colour, when there are no buffer stats to say it from", () => {
    // The card used to render an amber "none" here: a claim about a specific
    // encoder fault, made about a stream nobody examined.
    expect(keyframeVerdict(null, false)).toEqual({ verdict: "unknown", warn: false });
    expect(keyframeVerdict(undefined, true)).toEqual({ verdict: "unknown", warn: false });
  });

  it("reserves the amber for a RUNNING buffer that has genuinely seen no keyframe", () => {
    expect(keyframeVerdict({ videoFound: false }, true)).toEqual({ verdict: "none", warn: true });
  });

  it("does not warn about a stopped buffer holding no keyframe, which is by design", () => {
    expect(keyframeVerdict({ videoFound: false }, false)).toEqual({ verdict: "none", warn: false });
    expect(keyframeVerdict({ videoFound: false }, undefined)).toEqual({
      verdict: "none",
      warn: false,
    });
  });

  it("reports a keyframe that has arrived", () => {
    expect(keyframeVerdict({ videoFound: true }, true)).toEqual({ verdict: "seen", warn: false });
  });
});

describe("windowOnBufferToggle", () => {
  it("carries the typed window with the click that turns the buffer on", () => {
    // The switch sent a literal 0, which the server documents as "keep the
    // current window" -- so a window typed while Apply was greyed out (because
    // the buffer was off) was discarded by the very click meant to start it,
    // then overwritten in the input by the 3s poll. No message either way.
    expect(windowOnBufferToggle(true, 25)).toBe(25);
  });

  it("still means 'keep the current window' when turning the buffer off", () => {
    // Nothing to apply a window to, and sending one would silently commit an
    // edit the operator had not asked to commit.
    expect(windowOnBufferToggle(false, 25)).toBe(0);
  });

  it("falls back to the server's window rather than sending nonsense", () => {
    expect(windowOnBufferToggle(true, 0)).toBe(0);
    expect(windowOnBufferToggle(true, Number.NaN)).toBe(0);
    expect(windowOnBufferToggle(true, -5)).toBe(0);
  });
});

/* AND THAT THE PAGE ACTUALLY ASKS. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const clipsPage = () => readFileSync(join(ROOT, "ui/src/pages/ClipsPage.tsx"), "utf8");

describe("ClipsPage, wired to these decisions", () => {
  it("sends the typed window with the enable instead of discarding it", () => {
    const src = clipsPage();
    expect(src).toContain("onCheckedChange={(v) => setBuffer(v, windowOnBufferToggle(v, windowSec))}");
    expect(src).not.toContain("onCheckedChange={(v) => setBuffer(v, 0)}");
    // And the greyed Apply now says why, beside itself.
    expect(src).toContain('t("clips.windowAppliesOnEnable")');
  });

  it("dashes every buffer stat that has no snapshot behind it", () => {
    const src = clipsPage();
    // The five `?? 0` fabrications. "Ceiling 0 B" is not even a possible
    // reading: it is a configured constant that is never zero.
    expect(src).not.toContain("bytes(stats?.bytes ?? 0)");
    expect(src).not.toContain("bytes(stats?.maxBytes ?? 0)");
    expect(src).not.toContain("${stats?.bitrateKbps ?? 0} kbps");
    expect(src).not.toContain("value={`${held.toFixed(0)}s`}");
    expect(src).not.toContain('value={stats?.videoFound ? t("clips.seen") : t("clips.none")}');
    expect(src).toContain("keyframeVerdict(stats, buffer?.running)");
    expect(src).toContain('value={stats ? bytes(stats.maxBytes) : "—"}');
  });
});
