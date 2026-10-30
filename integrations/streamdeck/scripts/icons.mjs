// Draws the plugin's icons: key faces, action list icons, the category icon and
// the plugin icon. No image library and no network: each glyph is a set of
// shapes (circles, rounded rectangles, capsules, triangles, arcs) tested per
// sub-pixel, so the edges are anti-aliased and crisp at every size the manifest
// asks for. The output is committed, so the build never needs this script.
//
// Run: node scripts/icons.mjs
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "com.polyemesis.streamdeck.sdPlugin", "imgs");

// ---- colour -------------------------------------------------------------

/** OKLCH to sRGB 0..255, the colour space the UI's CSS variables are written in. */
function oklch(L, C, hDeg) {
  const h = (hDeg * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = L - 0.0894841775 * a - 1.291485548 * b;
  const l = l_ ** 3;
  const m = m_ ** 3;
  const s = s_ ** 3;
  const lin = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  const gamma = (x) => {
    const c = Math.min(1, Math.max(0, x));
    return c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
  };
  return lin.map((x) => Math.round(gamma(x) * 255));
}

/** The polyemesis accent: --primary in ui/src/index.css. */
const ACCENT = oklch(0.602, 0.1177, 263.2);
const INK = [255, 255, 255];

/** Key face colours by state, in the order KEY_STATE in src/render.ts defines them. */
const KEY_COLOURS = {
  idle: [58, 63, 71],
  live: [31, 157, 85],
  warn: [214, 158, 46],
  error: [197, 48, 48],
  held: [49, 130, 206],
  unbound: [45, 55, 72],
};

// ---- shapes, in unit coordinates (0..1 across the icon) -------------------

const inRoundRect = (x0, y0, x1, y1, r) => (x, y) => {
  if (x < x0 || x > x1 || y < y0 || y > y1) return false;
  const cx = Math.min(Math.max(x, x0 + r), x1 - r);
  const cy = Math.min(Math.max(y, y0 + r), y1 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
};
const inCircle = (cx, cy, r) => (x, y) => (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
const inRing = (cx, cy, r, w) => (x, y) => {
  const d2 = (x - cx) ** 2 + (y - cy) ** 2;
  return d2 <= (r + w / 2) ** 2 && d2 >= (r - w / 2) ** 2;
};
/** A ring segment between two angles in degrees, measured clockwise from the top. */
const inArc = (cx, cy, r, w, fromDeg, toDeg) => (x, y) => {
  if (!inRing(cx, cy, r, w)(x, y)) return false;
  let a = (Math.atan2(x - cx, cy - y) * 180) / Math.PI;
  if (a < 0) a += 360;
  return a >= fromDeg && a <= toDeg;
};
/** A straight bar with round ends. */
const inCapsule = (x0, y0, x1, y1, w) => (x, y) => {
  const dx = x1 - x0;
  const dy = y1 - y0;
  const t = Math.min(1, Math.max(0, ((x - x0) * dx + (y - y0) * dy) / (dx * dx + dy * dy)));
  return (x - (x0 + t * dx)) ** 2 + (y - (y0 + t * dy)) ** 2 <= (w / 2) ** 2;
};
const inTriangle = (pts) => (x, y) => {
  const sign = (p, q, r) => (p[0] - r[0]) * (q[1] - r[1]) - (q[0] - r[0]) * (p[1] - r[1]);
  const p = [x, y];
  const d1 = sign(p, pts[0], pts[1]);
  const d2 = sign(p, pts[1], pts[2]);
  const d3 = sign(p, pts[2], pts[0]);
  const neg = d1 < 0 || d2 < 0 || d3 < 0;
  const pos = d1 > 0 || d2 > 0 || d3 > 0;
  return !(neg && pos);
};
const any = (...fns) => (x, y) => fns.some((f) => f(x, y));

// ---- rasteriser ----------------------------------------------------------

const SAMPLES = 4;

/**
 * Renders one icon. `paint(x, y)` returns the colour of a sub-pixel as [r, g, b], or null
 * for transparent. Each pixel averages its sub-pixels, so an edge is a blend, not a step.
 */
function render(size, paint) {
  const out = Buffer.alloc(size * size * 4);
  const n = SAMPLES * SAMPLES;
  for (let py = 0; py < size; py++) {
    for (let px = 0; px < size; px++) {
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let sy = 0; sy < SAMPLES; sy++) {
        for (let sx = 0; sx < SAMPLES; sx++) {
          const x = (px + (sx + 0.5) / SAMPLES) / size;
          const y = (py + (sy + 0.5) / SAMPLES) / size;
          const c = paint(x, y);
          if (!c) continue;
          r += c[0];
          g += c[1];
          b += c[2];
          a += 1;
        }
      }
      const o = (py * size + px) * 4;
      if (a > 0) {
        out[o] = Math.round(r / a);
        out[o + 1] = Math.round(g / a);
        out[o + 2] = Math.round(b / a);
      }
      out[o + 3] = Math.round((a / n) * 255);
    }
  }
  return out;
}

/** A monochrome glyph: white where `shape` is inside, transparent elsewhere. */
const glyph = (shape) => (x, y) => (shape(x, y) ? INK : null);

/** A key face: a rounded tile in the state colour, lit a little from the top. */
function keyTile(base) {
  const tile = inRoundRect(0.02, 0.02, 0.98, 0.98, 0.14);
  return (x, y) => {
    if (!tile(x, y)) return null;
    const shade = 1.06 - 0.12 * y;
    return base.map((c) => Math.min(255, Math.round(c * shade)));
  };
}

/** The accent tile with a white mark, for the plugin icon. */
function brandTile(mark) {
  const tile = inRoundRect(0, 0, 1, 1, 0.2);
  return (x, y) => (tile(x, y) ? (mark(x, y) ? INK : ACCENT) : null);
}

// ---- glyphs --------------------------------------------------------------

// Destination: a transmitter with two broadcast arcs.
const GLYPH = {
  destination: any(
    inCircle(0.5, 0.62, 0.1),
    inArc(0.5, 0.62, 0.3, 0.09, 225, 315),
    inArc(0.5, 0.62, 0.46, 0.09, 225, 315),
  ),
  // Go live: a play triangle.
  "go-live": inTriangle([
    [0.34, 0.24],
    [0.34, 0.76],
    [0.8, 0.5],
  ]),
  // Stop all: a solid stop square.
  "stop-all": inRoundRect(0.26, 0.26, 0.74, 0.74, 0.06),
  // Stop destination: a stop square with a dot marking a single target.
  "stop-destination": any(inRoundRect(0.22, 0.3, 0.6, 0.68, 0.05), inCircle(0.76, 0.3, 0.1)),
  // Failover: two opposing arrows.
  failover: any(
    inCapsule(0.22, 0.36, 0.7, 0.36, 0.08),
    inTriangle([
      [0.7, 0.2],
      [0.82, 0.36],
      [0.7, 0.52],
    ]),
    inCapsule(0.3, 0.64, 0.78, 0.64, 0.08),
    inTriangle([
      [0.3, 0.48],
      [0.18, 0.64],
      [0.3, 0.8],
    ]),
  ),
  // Meter: three bars at different heights.
  meter: any(
    inRoundRect(0.18, 0.5, 0.36, 0.82, 0.03),
    inRoundRect(0.42, 0.26, 0.6, 0.82, 0.03),
    inRoundRect(0.66, 0.4, 0.84, 0.82, 0.03),
  ),
  // Record: a solid disc.
  record: inCircle(0.5, 0.5, 0.3),
  // Routing preset: three sliders with knobs set at different points.
  preset: any(
    inCapsule(0.16, 0.3, 0.84, 0.3, 0.06),
    inCapsule(0.16, 0.5, 0.84, 0.5, 0.06),
    inCapsule(0.16, 0.7, 0.84, 0.7, 0.06),
    inCircle(0.3, 0.3, 0.1),
    inCircle(0.66, 0.5, 0.1),
    inCircle(0.44, 0.7, 0.1),
  ),
};

// The category and plugin marks: a ring with a centre dot, the broadcast emblem.
const EMBLEM = any(inRing(0.5, 0.5, 0.3, 0.12), inCircle(0.5, 0.5, 0.11));

// A disc: the halted-disk mark on the record key's error face.
const DISC = any(inRing(0.5, 0.5, 0.3, 0.12), inCircle(0.5, 0.5, 0.07));

// ---- png -----------------------------------------------------------------

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

/** An RGBA PNG from a raw buffer of size*size*4 bytes. */
function encodePng(size, rgba) {
  const stride = size * 4;
  const raw = Buffer.alloc((stride + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (stride + 1)] = 0;
    rgba.copy(raw, y * (stride + 1) + 1, y * stride, (y + 1) * stride);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 6, 0, 0, 0], 8);
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function write(rel, size, paint) {
  const file = join(ROOT, rel);
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, encodePng(size, render(size, paint)));
}

// ---- the set -------------------------------------------------------------

// Each key action has its own set of six state faces. A face is the state tile with the
// action's glyph as a badge in the top-left corner, clear of the centred title text. The
// record key's error face (HALTED: DISK) carries a disc, not the record dot, so the halted
// state is a face of its own rather than the generic error tile.
const KEY_ACTIONS = ["destination", "go-live", "stop-all", "stop-destination", "record", "preset", "failover"];
const BADGE = { x: 0.06, y: 0.06, s: 0.3 };

function badged(base, shape) {
  const tile = keyTile(base);
  return (x, y) => {
    const c = tile(x, y);
    if (!c) return null;
    const bx = (x - BADGE.x) / BADGE.s;
    const by = (y - BADGE.y) / BADGE.s;
    if (bx >= 0 && bx <= 1 && by >= 0 && by <= 1 && shape(bx, by)) {
      return c.map((v) => Math.round(v + (255 - v) * 0.85));
    }
    return c;
  };
}

for (const action of KEY_ACTIONS) {
  for (const [state, colour] of Object.entries(KEY_COLOURS)) {
    const shape = action === "record" && state === "error" ? DISC : GLYPH[action];
    write(`keys/${action}/${state}.png`, 72, badged(colour, shape));
    write(`keys/${action}/${state}@2x.png`, 144, badged(colour, shape));
  }
}

for (const [name, shape] of Object.entries(GLYPH)) {
  write(`actions/${name}.png`, 20, glyph(shape));
  write(`actions/${name}@2x.png`, 40, glyph(shape));
}

write("category-icon.png", 28, glyph(EMBLEM));
write("category-icon@2x.png", 56, glyph(EMBLEM));
write("plugin-icon.png", 256, brandTile(EMBLEM));
write("plugin-icon@2x.png", 512, brandTile(EMBLEM));

console.log("wrote icons under", ROOT);
