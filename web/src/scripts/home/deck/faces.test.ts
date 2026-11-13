import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { FACE, destinationLayout, displayName } from "../../../../../integrations/streamdeck/src/keyImage.js";
import { stopTitle, titleFor } from "../../../../../integrations/streamdeck/src/render.js";
import type { KeyFace } from "../../../../../integrations/streamdeck/src/keyFace.js";
import { DESTS, REST, type DestState, type DeckState } from "./state";
import { TITLE_COLOURS } from "../../../../../integrations/streamdeck/scripts/keyPalette.mjs";
import { drawKey, glyphPath, keyFace, layoutFor, setTiles, tileFor, titleLines } from "./faces";

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

/* ----------------------------------------------------------------- drawKey */

type Fill = { text: string; fillStyle: unknown; font: string };
type Recorder = {
  ctx: CanvasRenderingContext2D;
  texts: Fill[];
  stops: Array<[number, string]>;
  images: unknown[];
  fills: number;
  clips: number;
};

/** A 2D context that records what a reader of the key would see: the strings drawn with the colour and
 *  font current at the moment of each fillText, the tile gradient's stops, and any image drawn. */
function recorder(widthOf: (font: string) => number = () => 10): Recorder {
  const r = { texts: [], stops: [], images: [], fills: 0, clips: 0 } as unknown as Recorder;
  const gradient = { addColorStop: (o: number, c: string) => r.stops.push([o, c]) };
  const state: Record<string, unknown> = { fillStyle: "", font: "" };
  const noop = () => {};
  const ctx = {
    setTransform: noop, fillRect: noop, beginPath: noop, roundRect: noop, save: noop, restore: noop, translate: noop, scale: noop, stroke: noop,
    fill: () => void r.fills++,
    clip: () => void r.clips++,
    createLinearGradient: () => gradient,
    createRadialGradient: () => gradient,
    measureText: () => ({ width: widthOf(String(state.font)) }),
    drawImage: (img: unknown) => r.images.push(img),
    fillText: (text: string) => r.texts.push({ text, fillStyle: state.fillStyle, font: String(state.font) }),
  };
  r.ctx = new Proxy(ctx, {
    get: (t, k: string) => (k in t ? (t as Record<string, unknown>)[k] : state[k]),
    set: (_t, k: string, v) => ((state[k] = v), true),
  }) as unknown as CanvasRenderingContext2D;
  return r;
}

const rgbOf = (c: readonly number[]) => `rgb(${c[0]},${c[1]},${c[2]})`;

describe("drawKey", () => {
  beforeAll(() => {
    // jsdom has no Path2D; the mark's path is drawn, not read back.
    (globalThis as unknown as { Path2D: unknown }).Path2D = class {};
  });
  afterEach(() => setTiles({}));

  const drawDest = (i: number, st: DestState) => {
    const rec = recorder();
    const face = keyFace(withDest(i, st), 3 + i);
    drawKey(rec.ctx, face);
    return { rec, face };
  };

  it("a destination key draws the name its layout says, in the layout's ink, then the plugin's title lines", () => {
    for (const st of STATES) {
      const { rec, face } = drawDest(0, st);
      const layout = layoutFor(face);
      expect(rec.texts[0]).toMatchObject({ text: layout.name?.text, fillStyle: rgbOf(layout.ink) });
      expect(rec.texts.slice(1).map((t) => t.text)).toEqual(titleLines(face));
    }
  });

  it("a destination whose layout has no name (X) draws only its title", () => {
    const { rec, face } = drawDest(4, "live");
    expect(layoutFor(face).name).toBeUndefined();
    expect(rec.texts.map((t) => t.text)).toEqual(titleLines(face));
  });

  it("a failed destination draws its cause under its state, in the plugin's title colour, on a tile that is not the live one", () => {
    const failed = drawDest(1, "failed");
    const live = drawDest(1, "live");
    expect(failed.rec.texts.map((t) => t.text).slice(-2)).toEqual(["Failed", "Timed out"]);
    expect(failed.rec.texts.at(-1)?.fillStyle).toBe(rgbOf(TITLE_COLOURS.error));
    expect(live.rec.texts.at(-1)?.fillStyle).toBe(rgbOf(TITLE_COLOURS.live));
    const fl = layoutFor(failed.face);
    // The first two stops are the tile's own gradient; the rest are the LCD falloff.
    expect(failed.rec.stops.slice(0, 2)).toEqual([[0, rgbOf(fl.tileTop)], [1, rgbOf(fl.tileBottom)]]);
    expect(failed.rec.stops.slice(0, 2)).not.toEqual(live.rec.stops.slice(0, 2));
  });

  it("each destination state draws the title colour the plugin's manifest gives that state", () => {
    const tones = ["idle", "warn", "live", "error"] as const;
    STATES.forEach((st, n) => {
      const { rec } = drawDest(2, st);
      expect(rec.texts.at(-1)?.fillStyle).toBe(rgbOf(TITLE_COLOURS[tones[n]]));
    });
  });

  it("a control key draws the plugin's tile image when it is loaded, and only its title otherwise", () => {
    const img = { tile: "go-live/idle" } as unknown as CanvasImageSource;
    setTiles({ "go-live/idle": img });
    const withTile = recorder();
    drawKey(withTile.ctx, keyFace(REST, 0));
    expect(withTile.images).toEqual([img]);
    expect(withTile.texts.map((t) => t.text)).toEqual(["Go live"]);

    setTiles({});
    const without = recorder();
    drawKey(without.ctx, keyFace(REST, 0));
    expect(without.images).toEqual([]);
    expect(without.texts.map((t) => t.text)).toEqual(["Go live"]);
  });

  it("the held Stop all face draws the held tile and the plugin's stopTitle lines in the held colour", () => {
    const img = {} as CanvasImageSource;
    setTiles({ "stop-all/held": img });
    const rec = recorder();
    const s: DeckState = { ...REST, dests: ["live", "off", "off", "off", "off"], held: "stopall" };
    drawKey(rec.ctx, keyFace(s, 1));
    expect(rec.images).toEqual([img]);
    expect(rec.texts.map((t) => t.text)).toEqual(stopTitle(1, "en").split("\n"));
    expect(rec.texts.every((t) => t.fillStyle === rgbOf(TITLE_COLOURS.held))).toBe(true);
  });

  it("the held Routing preset face reads Applying and the preset's name", () => {
    const rec = recorder();
    drawKey(rec.ctx, keyFace({ ...REST, held: "preset" }, 2));
    expect(rec.texts.map((t) => t.text)).toEqual(["Applying", "No music"]);
  });

  it("an unknown tone falls back to the idle title colour instead of drawing in nothing", () => {
    const rec = recorder();
    drawKey(rec.ctx, { tone: "bogus", label: "streamdeck.go.live" } as unknown as KeyFace);
    expect(rec.texts[0].fillStyle).toBe(rgbOf(TITLE_COLOURS.idle));
  });

  it("a title wider than the key is drawn smaller until it fits, and never below the floor", () => {
    const px = (font: string) => Number(/(\d+(?:\.\d+)?)px/.exec(font)?.[1]);
    const KEY = 256;
    const SCALE_ = KEY / FACE;
    const fits = recorder((f) => px(f) * 8); // 8 em wide: only a smaller size fits
    drawKey(fits.ctx, keyFace(REST, 0));
    const fitted = px(fits.texts[0].font);
    expect(fitted).toBeLessThan(22 * SCALE_);
    expect(fitted * 8).toBeLessThanOrEqual(KEY - 12 * SCALE_);
    const never = recorder(() => 10_000); // nothing fits: it stops at the floor
    drawKey(never.ctx, keyFace(REST, 0));
    // It shrinks in whole pixels and stops at the first size at or under the floor.
    expect(px(never.texts[0].font)).toBeLessThanOrEqual(13 * SCALE_);
    expect(px(never.texts[0].font)).toBeGreaterThan(13 * SCALE_ - 1);
  });
});
