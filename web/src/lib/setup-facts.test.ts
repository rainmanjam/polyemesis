import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { RECIPES } from "../data/recipes.mjs";
import * as SETUP from "../data/setup-facts.mjs";
import type { Fact, Step } from "./facts";

/** Every fact either family of pages states, with the page it is on. */
function allFacts(): { page: string; fact: Fact }[] {
  const out: { page: string; fact: Fact }[] = [];
  for (const a of Object.values(AUDIENCES) as { key: string; needs: Fact[]; steps: Step[] }[]) {
    for (const f of [...a.needs, ...a.steps.flatMap((s) => s.facts)]) out.push({ page: a.key, fact: f });
  }
  for (const r of RECIPES as { slug: string; needs: Fact[]; steps: Step[]; check: Fact[] }[]) {
    for (const f of [...r.needs, ...r.steps.flatMap((s) => s.facts), ...r.check]) out.push({ page: r.slug, fact: f });
  }
  return out;
}

/** An action the audience pages and the recipes both state, and the one Fact that states it. A sentence on any page
 *  that mentions the action must be that object: a second wording is how two pages drift apart. */
const SHARED_ACTIONS: [string, RegExp, Fact][] = [
  ["bitrate for each track", /bitrate for each track/i, SETUP.OBS_BITRATES],
  ["Advanced Audio Properties", /Advanced Audio Properties/, SETUP.OBS_TRACK_TICKS],
  ["add a source", /\badd a source\b|source, added on the Sources page/i, SETUP.ADD_SOURCE],
  ["publish URL", /publish URL/, SETUP.COPY_URL],
  ["Custom Output (FFmpeg)", /Custom Output \(FFmpeg\)/, SETUP.OBS_CUSTOM_OUTPUT],
  ["File path or URL", /File path or URL/, SETUP.PASTE_URL],
  ["Audio Track ticks", /Audio Track/, SETUP.AUDIO_TRACKS],
  ["Start Recording", /Start Recording/, SETUP.START_RECORDING],
  ["a meter for every incoming track", /meter for every incoming track/, SETUP.METERS_APPEAR],
  ["Classic RTMP's single track", /Classic RTMP/, SETUP.SRT_NEED],
  ["annotations", /annotations/, SETUP.LABEL_TRACKS],
  ["the hostname for an account", /real hostname/, SETUP.CONNECT_NEEDS_HOSTNAME],
  ["the redirect URL", /redirect URL has the same form/, SETUP.REDIRECT_URL],
  ["register an app", /Register an app/, SETUP.REGISTER_APP],
  ["Connect account", /Connect account/, SETUP.PASTE_CREDENTIALS],
  ["Destinations → Add", /Destinations → Add/, SETUP.ADD_DESTINATION],
  ["the destination's URL and key", /stream key from the platform's (own )?dashboard/, SETUP.URL_AND_KEY],
  ["tick the tracks", /tick the ones/i, SETUP.TICK_TRACKS],
  ["enable it", /^Enable (the|each) destination/, SETUP.ENABLE_DESTINATION],
  ["measured after routing", /measured after routing/, SETUP.METERS_AFTER_ROUTING],
  ["silence", /not sending gets silence/, SETUP.SILENCE_FACT],
];

describe("setup facts", () => {
  it.each(SHARED_ACTIONS)("states %s once, as the shared Fact", (_name, re, shared) => {
    // KEY_OR_ACCOUNT is the need that precedes URL_AND_KEY: a need, not the step.
    for (const { page, fact } of allFacts()) {
      if (fact === SETUP.KEY_OR_ACCOUNT_NEED) continue;
      if (re.test(fact.text)) expect(fact, `${page}: "${fact.text}"`).toBe(shared);
    }
  });

  it("is used: every shared fact appears on at least one page", () => {
    const used = new Set(allFacts().map((x) => x.fact));
    for (const [name, fact] of Object.entries(SETUP)) {
      if (typeof fact === "object" && fact !== null && "evidence" in fact) expect(used.has(fact as Fact), name).toBe(true);
    }
  });
});
