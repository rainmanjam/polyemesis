import { describe, expect, it } from "vitest";
import { destinationLayout, displayName } from "../../../../../integrations/streamdeck/src/keyImage.js";
import { stopTitle, titleFor } from "../../../../../integrations/streamdeck/src/render.js";
import type { KeyFace } from "../../../../../integrations/streamdeck/src/keyFace.js";
import { DESTS, REST, type DestState, type DeckState } from "./state";
import { glyphPath, keyFace, layoutFor, tileFor, titleLines } from "./faces";

const STATES: DestState[] = ["off", "starting", "live", "failed"];
const withDest = (i: number, st: DestState): DeckState => ({ ...REST, dests: REST.dests.map((d, j) => (j === i ? st : d)) as DeckState["dests"] });

describe("destination keys", () => {
  for (const [i, d] of DESTS.entries()) {
    for (const st of STATES) {
      it(`${d.platform} ${st}: the layout is the plugin's destinationLayout, from the plugin's own title`, () => {
        const face = keyFace(withDest(i, st), 3 + i);
        expect(face.mark).toEqual({ platform: d.platform, name: d.name });
        const lines = titleFor(face, "en").split("\n");
        expect(titleLines(face)).toEqual(lines);
        expect(layoutFor(face)).toEqual(destinationLayout(face.tone, { platform: d.platform, name: d.name }, lines.length));
      });
    }
  }

  it("the tone of each state is the plugin's: off idle, starting warn, live live, failed error", () => {
    expect(STATES.map((st) => keyFace(withDest(0, st), 3).tone)).toEqual(["idle", "warn", "live", "error"]);
  });

  it("a failed destination says why, on the second line, in the plugin's words", () => {
    expect(titleFor(keyFace(withDest(1, "failed"), 4), "en")).toBe("Failed\nTimed out");
  });

  it("X's key leaves the name off (the mark is the name) and Club TV's keeps it", () => {
    expect(layoutFor(keyFace(REST, 7)).name).toBeUndefined();
    expect(layoutFor(keyFace(REST, 3)).name?.text).toBe(displayName({ platform: "youtube", name: "Club TV" }));
  });

  it("every platform has a mark path to draw", () => {
    for (const d of DESTS) expect(glyphPath(layoutFor(keyFace(REST, 3 + DESTS.indexOf(d))))).toMatch(/^M/);
  });
});

describe("control keys", () => {
  it("Go live, Stop all and Routing preset at rest read as the plugin's idle faces", () => {
    expect([0, 1, 2].map((k) => titleLines(keyFace(REST, k)))).toEqual([["Go live"], ["Hold to stop"], ["Hold to apply"]]);
    expect([0, 1, 2].map((k) => keyFace(REST, k).tone)).toEqual(["idle", "idle", "idle"]);
  });

  it("the Stop all held face for one live YouTube destination is the plugin's stopTitle(1)", () => {
    const only = { ...REST, dests: ["live", "off", "off", "off", "off"] as DeckState["dests"], held: "stopall" as const };
    const face: KeyFace = keyFace(only, 1);
    expect(face.tone).toBe("held");
    expect(titleFor(face, "en")).toBe(stopTitle(1, "en"));
    expect(titleLines(face)).toEqual(["Hold to stop", "1 YouTube will end"]);
  });

  it("with no YouTube destination live the held face says so, as stopTitle(0) does", () => {
    const none = { ...REST, dests: ["off", "live", "live", "live", "live"] as DeckState["dests"], held: "stopall" as const };
    expect(titleFor(keyFace(none, 1), "en")).toBe(stopTitle(0, "en"));
  });

  it("the preset held face reads Applying, and is the held tile", () => {
    const f = keyFace({ ...REST, held: "preset" }, 2);
    expect(f.tone).toBe("held");
    expect(titleLines(f)[0]).toBe("Applying");
  });

  it("each control key names the plugin's own tile image for its tone", () => {
    expect(tileFor(keyFace(REST, 0))).toBe("go-live/idle");
    expect(tileFor(keyFace(REST, 1))).toBe("stop-all/idle");
    expect(tileFor(keyFace({ ...REST, held: "stopall" }, 1))).toBe("stop-all/held");
    expect(tileFor(keyFace({ ...REST, held: "preset" }, 2))).toBe("preset/held");
    expect(tileFor(keyFace(REST, 3))).toBeUndefined();
  });
});
