// WCAG AA for every key title. Stream Deck draws the title in white (the manifest
// sets no TitleColor), centred on the state tile, so every tile colour has to give
// white text at least 4.5:1, at the tile's lightest point.
//
// Two checks, because they catch different mistakes:
//   - the palette: a colour edited in scripts/keyPalette.mjs that fails;
//   - the committed PNGs: a palette fixed but `node scripts/icons.mjs` never re-run,
//     so the faces on the device are still the old ones.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { inflateSync } from "node:zlib";
import { describe, expect, it } from "vitest";
import { KEY_STATE } from "../src/render.js";
import { contrast, KEY_COLOURS, SHADE_SLOPE, SHADE_TOP, TITLE_INK } from "../scripts/keyPalette.mjs";

const AA = 4.5;
const IMGS = join(dirname(fileURLToPath(import.meta.url)), "..", "com.polyemesis.streamdeck.sdPlugin", "imgs");

/** The lightest the tile gets: the top edge, where the lighting multiplies the colour most. */
const lightest = (rgb: readonly number[]) => rgb.map((c) => Math.min(255, Math.round(c * SHADE_TOP)));

describe("key face palette", () => {
  it("has a colour for every key state and no other", () => {
    expect(Object.keys(KEY_COLOURS).sort()).toEqual(Object.keys(KEY_STATE).sort());
  });

  it("darkens down the tile, so the top edge is the worst case", () => {
    expect(SHADE_TOP).toBeGreaterThan(SHADE_TOP - SHADE_SLOPE);
  });

  for (const [state, rgb] of Object.entries(KEY_COLOURS)) {
    it(`${state}: a white title reads at ${AA}:1 or better`, () => {
      expect(contrast(lightest(rgb), TITLE_INK)).toBeGreaterThanOrEqual(AA);
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
// lightened toward white, clear of the centred title. It is the glyph, not the tile, so it
// is left out; a pixel's margin keeps its anti-aliased edge out too.
const inBadge = (x: number, y: number, size: number) => {
  const m = 1 / size;
  return x >= 0.06 - m && x <= 0.36 + m && y >= 0.06 - m && y <= 0.36 + m;
};

describe("committed key faces", () => {
  const actions = readdirSync(join(IMGS, "keys"));
  it("finds the key faces", () => {
    expect(actions.length).toBeGreaterThan(0);
  });

  for (const action of actions) {
    for (const file of readdirSync(join(IMGS, "keys", action))) {
      it(`${action}/${file}: white on its lightest tile pixel is ${AA}:1 or better`, () => {
        const { size, px } = decode(join(IMGS, "keys", action, file));
        let worst = Infinity;
        for (let y = 0; y < size; y++) {
          for (let x = 0; x < size; x++) {
            const o = (y * size + x) * 4;
            if (px[o + 3] !== 255) continue;
            if (inBadge((x + 0.5) / size, (y + 0.5) / size, size)) continue;
            worst = Math.min(worst, contrast([px[o]!, px[o + 1]!, px[o + 2]!], TITLE_INK));
          }
        }
        expect(worst).toBeGreaterThanOrEqual(AA);
      });
    }
  }
});
