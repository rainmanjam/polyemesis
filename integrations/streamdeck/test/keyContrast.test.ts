// WCAG AA for every key title. The plugin sets a key's title TEXT with setTitle;
// Stream Deck draws it in the TitleColor the manifest gives the key's current state
// (white everywhere but warn, near-black on warn's bright amber). So the colour a
// title really has is the manifest's, and each state's tile has to give it at least
// 4.5:1 at the tile's worst point: the lightest pixel for a white title, the darkest
// for a dark one.
//
// Three checks, because they catch different mistakes:
//   - the manifest: a TitleColor missing, or out of step with keyPalette.mjs;
//   - the palette: a colour edited in scripts/keyPalette.mjs that fails;
//   - the committed PNGs: a palette fixed but `node scripts/icons.mjs` never re-run,
//     so the faces on the device are still the old ones.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { inflateSync } from "node:zlib";
import { describe, expect, it } from "vitest";
import { KEY_STATE } from "../src/render.js";
import {
  contrast,
  hex,
  KEY_COLOURS,
  SHADE_SLOPE,
  SHADE_TOP,
  TITLE_COLOURS,
  TITLE_WHITE,
} from "../scripts/keyPalette.mjs";

const AA = 4.5;
const PLUGIN = join(dirname(fileURLToPath(import.meta.url)), "..", "com.polyemesis.streamdeck.sdPlugin");
const IMGS = join(PLUGIN, "imgs");

interface ManifestAction {
  UUID: string;
  Controllers: string[];
  States: { Image: string; TitleColor?: string }[];
}
const manifest = JSON.parse(readFileSync(join(PLUGIN, "manifest.json"), "utf8")) as { UUID: string; Actions: ManifestAction[] };
const keyActions = manifest.Actions.filter((a) => a.Controllers.includes("Keypad"));
const STATE_BY_INDEX = Object.fromEntries(Object.entries(KEY_STATE).map(([name, i]) => [i, name])) as Record<number, string>;

const parseHex = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));

/** The tile's colour at its lightest (top edge) and darkest (bottom edge) points. */
const shaded = (rgb: readonly number[], y: number) =>
  rgb.map((c) => Math.min(255, Math.round(c * (SHADE_TOP - SHADE_SLOPE * y))));

describe("key title colours in the manifest", () => {
  it("has key actions to check", () => {
    expect(keyActions.length).toBeGreaterThan(0);
  });

  for (const a of keyActions) {
    it(`${a.UUID}: every state sets the title colour keyPalette.mjs gives it`, () => {
      expect(a.States).toHaveLength(Object.keys(KEY_STATE).length);
      a.States.forEach((st, i) => {
        const state = STATE_BY_INDEX[i]!;
        expect(st.TitleColor, `${a.UUID} state ${i} (${state})`).toBe(hex(TITLE_COLOURS[state]!));
      });
    });
  }

  it("draws only warn's title dark; every other state is white", () => {
    for (const [state, ink] of Object.entries(TITLE_COLOURS)) {
      if (state === "warn") expect(ink).not.toEqual(TITLE_WHITE);
      else expect(ink, state).toEqual(TITLE_WHITE);
    }
  });
});

describe("key face palette", () => {
  it("has a colour and a title colour for every key state and no other", () => {
    expect(Object.keys(KEY_COLOURS).sort()).toEqual(Object.keys(KEY_STATE).sort());
    expect(Object.keys(TITLE_COLOURS).sort()).toEqual(Object.keys(KEY_STATE).sort());
  });

  it("darkens down the tile, so the edges are the extremes", () => {
    expect(SHADE_SLOPE).toBeGreaterThan(0);
  });

  for (const [state, rgb] of Object.entries(KEY_COLOURS)) {
    it(`${state}: its title reads at ${AA}:1 or better anywhere on the tile`, () => {
      const ink = TITLE_COLOURS[state]!;
      const worst = Math.min(contrast(shaded(rgb, 0), ink), contrast(shaded(rgb, 1), ink));
      expect(worst).toBeGreaterThanOrEqual(AA);
    });
  }
});

/** Decodes the RGBA PNGs scripts/icons.mjs writes: 8-bit, colour type 6, filter 0 on every row. */
function decode(file: string): { size: number; px: Buffer } {
  const buf = readFileSync(file);
  let off = 8;
  let size = 0;
  const idat: Buffer[] = [];
  while (off < buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.toString("ascii", off + 4, off + 8);
    const data = buf.subarray(off + 8, off + 8 + len);
    if (type === "IHDR") {
      size = data.readUInt32BE(0);
      expect(data[8], `${file} bit depth`).toBe(8);
      expect(data[9], `${file} colour type`).toBe(6);
    }
    if (type === "IDAT") idat.push(data);
    off += 12 + len;
  }
  const raw = inflateSync(Buffer.concat(idat));
  const stride = size * 4;
  const px = Buffer.alloc(stride * size);
  for (let y = 0; y < size; y++) {
    expect(raw[y * (stride + 1)], `${file} row ${y} filter`).toBe(0);
    raw.copy(px, y * stride, y * (stride + 1) + 1, (y + 1) * (stride + 1));
  }
  return { size, px };
}

// The action badge sits in the top-left corner (BADGE in scripts/icons.mjs, 0.06..0.36),
// clear of the centred title. It is the glyph, not the tile, so it is left out; a pixel's
// margin keeps its anti-aliased edge out too.
const inBadge = (x: number, y: number, size: number) => {
  const m = 1 / size;
  return x >= 0.06 - m && x <= 0.36 + m && y >= 0.06 - m && y <= 0.36 + m;
};

describe("committed key faces, against the title colour the manifest gives their state", () => {
  for (const a of keyActions) {
    a.States.forEach((st, i) => {
      for (const file of [`${st.Image}.png`, `${st.Image}@2x.png`]) {
        it(`${file}: its title reads at ${AA}:1 or better on every tile pixel`, () => {
          const ink = parseHex(st.TitleColor ?? "#FFFFFF");
          const { size, px } = decode(join(PLUGIN, file));
          let worst = Infinity;
          for (let y = 0; y < size; y++) {
            for (let x = 0; x < size; x++) {
              const o = (y * size + x) * 4;
              if (px[o + 3] !== 255) continue;
              if (inBadge((x + 0.5) / size, (y + 0.5) / size, size)) continue;
              worst = Math.min(worst, contrast([px[o]!, px[o + 1]!, px[o + 2]!], ink));
            }
          }
          expect(worst, `${a.UUID} state ${i}`).toBeGreaterThanOrEqual(AA);
        });
      }
    });
  }

  it("covers every key face on disk", () => {
    const onDisk = readdirSync(join(IMGS, "keys")).flatMap((d) =>
      readdirSync(join(IMGS, "keys", d)).map((f) => `imgs/keys/${d}/${f}`),
    );
    const checked = keyActions.flatMap((a) => a.States.flatMap((st) => [`${st.Image}.png`, `${st.Image}@2x.png`]));
    expect(new Set(checked)).toEqual(new Set(onDisk));
  });
});
