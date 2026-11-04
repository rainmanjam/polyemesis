import { PLATFORM_NAME, type KeyTone } from "./keyFace.js";
import { KEY_COLOURS, SHADE_SLOPE, SHADE_TOP, TITLE_COLOURS, type Rgb } from "../scripts/keyPalette.mjs";

// A DESTINATION KEY SAYS WHICH DESTINATION IT IS. The state tile, the platform's mark and the
// destination's name are drawn into the key image (setImage); the state and its reason stay the
// title (setTitle), drawn by Stream Deck at the bottom in the state's TitleColor, as on every key.
// So the name sits in the top band, where the action badge used to be, and never takes a title line.
//
// The image is the whole face, not a layer: setImage replaces the state's PNG. So it draws the same
// tile scripts/icons.mjs draws (KEY_COLOURS, lit from the top by SHADE_TOP/SHADE_SLOPE, the same
// rounded corners), and the mark and name in the state's TITLE colour, which test/keyContrast.test.ts
// already holds to WCAG AA on that tile and test/keyImage.test.ts checks again for the top band.
//
// The marks are Simple Icons (https://simpleicons.org), CC0 1.0, the same paths as
// ui/src/components/platformGlyphs.tsx, copied verbatim from the published SVGs. The logos remain
// the trade marks of their owners and are used only to identify the service a destination streams
// to. See com.polyemesis.streamdeck.sdPlugin/third-party-licenses.txt.

/** The destination a key is about: the server's platform id and the operator's name for it. */
export interface DestinationMark {
  platform: string;
  name: string;
}

function round(n: number): number {
  return Math.round(n * 1000) / 1000;
}

/** Every unit below is in the face's own 144-unit square (the @2x key); the SVG scales to 72 px. */
export const FACE = 144;

/** The tile, as scripts/icons.mjs draws it: inset 0.02 of the key, corner radius 0.14. */
const TILE_INSET = 0.02 * FACE;
const TILE_RADIUS = 0.14 * FACE;

/** The top band: the mark at the left, the name beside it, both clear of a three-line title. */
export const BAND = { x: 11, y: 11, glyph: 30, gap: 7, right: 11 } as const;
/** The name's largest and smallest sizes. 22/144 is the title's own size (11 px on a 72 px key). */
export const NAME_MAX_PX = 22;
export const NAME_MIN_PX = 15;
/** A title of this many lines reaches the band: the name is left off, and the mark stays, as the badge did. */
export const NAME_TITLE_LINES_MAX = 3;

export type GlyphId = "youtube" | "twitch" | "kick" | "facebook" | "x" | "destination";

/** Simple Icons paths, 24-unit viewBox. */
const SIMPLE_ICONS: Record<Exclude<GlyphId, "destination">, string> = {
  youtube:
    "M23.498 6.186a3.016 3.016 0 0 0-2.122-2.136C19.505 3.545 12 3.545 12 3.545s-7.505 0-9.377.505A3.017 3.017 0 0 0 .502 6.186C0 8.07 0 12 0 12s0 3.93.502 5.814a3.016 3.016 0 0 0 2.122 2.136c1.871.505 9.376.505 9.376.505s7.505 0 9.377-.505a3.015 3.015 0 0 0 2.122-2.136C24 15.93 24 12 24 12s0-3.93-.502-5.814zM9.545 15.568V8.432L15.818 12l-6.273 3.568z",
  twitch:
    "M11.571 4.714h1.715v5.143H11.57zm4.715 0H18v5.143h-1.714zM6 0L1.714 4.286v15.428h5.143V24l4.286-4.286h3.428L22.286 12V0zm14.571 11.143l-3.428 3.428h-3.429l-3 3v-3H6.857V1.714h13.714Z",
  facebook:
    "M9.101 23.691v-7.98H6.627v-3.667h2.474v-1.58c0-4.085 1.848-5.978 5.858-5.978.401 0 .955.042 1.468.103a8.68 8.68 0 0 1 1.141.195v3.325a8.623 8.623 0 0 0-.653-.036 26.805 26.805 0 0 0-.733-.009c-.707 0-1.259.096-1.675.309a1.686 1.686 0 0 0-.679.622c-.258.42-.374.995-.374 1.752v1.297h3.919l-.386 2.103-.287 1.564h-3.246v8.245C19.396 23.238 24 18.179 24 12.044c0-6.627-5.373-12-12-12s-12 5.373-12 12c0 5.628 3.874 10.35 9.101 11.647Z",
  kick: "M1.333 0h8v5.333H12V2.667h2.667V0h8v8H20v2.667h-2.667v2.666H20V16h2.667v8h-8v-2.667H12v-2.666H9.333V24h-8Z",
  x: "M18.901 1.153h3.68l-8.04 9.19L24 22.846h-7.406l-5.8-7.584-6.638 7.584H.474l8.6-9.83L0 1.154h7.594l5.243 6.932ZM17.61 20.644h2.039L6.486 3.24H4.298Z",
};

/**
 * The neutral mark for a custom RTMP destination, and for any platform without a mark here: the
 * transmitter with two broadcast arcs that scripts/icons.mjs draws as the destination badge, in the
 * same 24-unit box. Ours, not a brand's.
 */
const TRANSMITTER = {
  dot: { cx: 12, cy: 14.88, r: 2.4 },
  arcs: [7.2, 11.04].map((r) => {
    // From 225 to 315 degrees, clockwise from the top: the left-hand quarter, as in icons.mjs.
    const at = (deg: number) => {
      const a = (deg * Math.PI) / 180;
      return [round(12 + r * Math.sin(a)), round(14.88 - r * Math.cos(a))] as const;
    };
    const [x0, y0] = at(225);
    const [x1, y1] = at(315);
    return `M${x0} ${y0}A${r} ${r} 0 0 1 ${x1} ${y1}`;
  }),
  width: 2.16,
};

/** The platform ids the server sends (internal/db/destinations.go) that have a mark here. */
export function glyphFor(platform: string): GlyphId {
  return Object.hasOwn(SIMPLE_ICONS, platform) ? (platform as GlyphId) : "destination";
}

/** What the face draws, as data: test/keyImage.test.ts reads this, destinationImage serialises it. */
export interface FaceLayout {
  tone: KeyTone;
  /** The tile's colour at its top and bottom edge (the gradient's stops). */
  tileTop: Rgb;
  tileBottom: Rgb;
  /** The mark's and the name's colour: the state's title colour. */
  ink: Rgb;
  glyph: { id: GlyphId; x: number; y: number; size: number };
  /** Absent when the title is tall enough to reach the band. */
  name?: { text: string; x: number; baseline: number; fontSize: number; width: number };
}

const shade = (c: Rgb, k: number): Rgb => c.map((v) => Math.min(255, Math.round(v * k))) as Rgb;

/**
 * The advance of one character as a fraction of the font size, for a bold sans: a deliberate
 * over-estimate, so a name that is drawn fits. Wide scripts (CJK, kana, hangul, full-width forms)
 * count as a full em.
 */
export function advance(ch: string): number {
  const cp = ch.codePointAt(0) ?? 0;
  if (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe4f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    cp >= 0x1f000
  ) {
    return 1;
  }
  if (" ".includes(ch)) return 0.28;
  if ("iIljtf!.,:;'|()[]r1".includes(ch)) return 0.32;
  if ("mwMW@%".includes(ch)) return 0.9;
  if (ch >= "A" && ch <= "Z") return 0.72;
  return 0.58;
}

export const textWidth = (text: string, px: number): number => [...text].reduce((w, c) => w + advance(c), 0) * px;

/** The name as it fits the band: the largest size down to NAME_MIN_PX, then cut with an ellipsis. */
export function fitName(text: string, room: number): { text: string; fontSize: number; width: number } {
  for (let px = NAME_MAX_PX; px >= NAME_MIN_PX; px -= 1) {
    const w = textWidth(text, px);
    if (w <= room) return { text, fontSize: px, width: w };
  }
  const chars = [...text];
  while (chars.length > 1 && textWidth(`${chars.join("")}…`, NAME_MIN_PX) > room) chars.pop();
  const cut = `${chars.join("").trimEnd()}…`;
  return { text: cut, fontSize: NAME_MIN_PX, width: textWidth(cut, NAME_MIN_PX) };
}

/** A code point XML 1.0 can carry: anything else would make the SVG malformed, escaped or not. */
function xmlChar(cp: number): boolean {
  return (
    cp === 0x9 || cp === 0xa || cp === 0xd || (cp >= 0x20 && cp <= 0xd7ff) || (cp >= 0xe000 && cp <= 0xfffd) || (cp >= 0x10000 && cp <= 0x10ffff)
  );
}

/** The name a key shows: the destination's own, else the platform's, without control characters or
 *  anything XML cannot carry (U+FFFE, a lone surrogate), each of which becomes a space. */
export function displayName(mark: DestinationMark): string {
  const clean = [...mark.name].map((ch) => {
    const cp = ch.codePointAt(0) ?? 0;
    return xmlChar(cp) && cp !== 0x7f && cp >= 0x20 ? ch : " ";
  });
  const own = clean.join("").replace(/\s+/g, " ").trim();
  return own || PLATFORM_NAME[mark.platform] || mark.platform;
}

/**
 * Platforms whose mark IS their name: X's mark is the letter X, so a key named "X" would read "X X".
 * On these, a name that only repeats the platform's own name is left off and the mark stands alone.
 * A name of the operator's own ("X — clips") is still drawn. YouTube's or Twitch's mark is a symbol,
 * not its name, so "YouTube" beside the play button is not a repeat and stays.
 */
const MARK_IS_NAME = new Set(["x"]);

/** True when the name would only repeat what the mark already says. */
export function nameRepeatsMark(mark: DestinationMark, text: string): boolean {
  if (!MARK_IS_NAME.has(mark.platform)) return false;
  const own = PLATFORM_NAME[mark.platform];
  return own !== undefined && text.toLocaleLowerCase() === own.toLocaleLowerCase();
}

/** The layout of a destination key's image for a state, its destination and its title's line count. */
export function destinationLayout(tone: KeyTone, mark: DestinationMark, titleLines: number): FaceLayout {
  const base = KEY_COLOURS[tone] as Rgb;
  const glyph = { id: glyphFor(mark.platform), x: BAND.x, y: BAND.y, size: BAND.glyph };
  const layout: FaceLayout = {
    tone,
    tileTop: shade(base, SHADE_TOP),
    tileBottom: shade(base, SHADE_TOP - SHADE_SLOPE),
    ink: TITLE_COLOURS[tone] as Rgb,
    glyph,
  };
  const text = displayName(mark);
  if (titleLines <= NAME_TITLE_LINES_MAX && text && !nameRepeatsMark(mark, text)) {
    const x = BAND.x + BAND.glyph + BAND.gap;
    const fit = fitName(text, FACE - BAND.right - x);
    // Centred on the mark: a cap-height of about 0.72 em puts the baseline 0.36 em below the middle.
    layout.name = { ...fit, x, baseline: round(BAND.y + BAND.glyph / 2 + fit.fontSize * 0.36) };
  }
  return layout;
}

const rgb = (c: Rgb) => `rgb(${c[0]},${c[1]},${c[2]})`;
const xml = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;");

/** The layout as an SVG document. */
export function layoutSvg(l: FaceLayout): string {
  const ink = rgb(l.ink);
  const k = l.glyph.size / 24;
  const glyph =
    l.glyph.id === "destination"
      ? `<circle cx="${TRANSMITTER.dot.cx}" cy="${TRANSMITTER.dot.cy}" r="${TRANSMITTER.dot.r}" fill="${ink}"/>` +
        TRANSMITTER.arcs.map((d) => `<path d="${d}" fill="none" stroke="${ink}" stroke-width="${TRANSMITTER.width}"/>`).join("")
      : `<path d="${SIMPLE_ICONS[l.glyph.id]}" fill="${ink}"/>`;
  const name = l.name
    ? `<text x="${l.name.x}" y="${l.name.baseline}" font-family="Helvetica Neue, Helvetica, Arial, sans-serif" font-weight="700" font-size="${l.name.fontSize}" fill="${ink}">${xml(l.name.text)}</text>`
    : "";
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${FACE}" height="${FACE}" viewBox="0 0 ${FACE} ${FACE}">` +
    `<defs><linearGradient id="tile" x1="0" y1="0" x2="0" y2="${FACE}" gradientUnits="userSpaceOnUse">` +
    `<stop offset="0" stop-color="${rgb(l.tileTop)}"/><stop offset="1" stop-color="${rgb(l.tileBottom)}"/></linearGradient></defs>` +
    `<rect x="${round(TILE_INSET)}" y="${round(TILE_INSET)}" width="${round(FACE - 2 * TILE_INSET)}" height="${round(FACE - 2 * TILE_INSET)}" rx="${round(TILE_RADIUS)}" fill="url(#tile)"/>` +
    `<g transform="translate(${l.glyph.x} ${l.glyph.y}) scale(${round(k)})">${glyph}</g>` +
    name +
    `</svg>`
  );
}

/**
 * Sends a key's image only when it, or the key's state, has changed since the last image that was
 * accepted. Keyed on the pair, because Stream Deck applies an image to a state: after a state change
 * the image is sent again, whichever states the app applied the last one to. A send that fails is
 * forgotten, so the next paint sends again; a stale mark can never be left over a padlock or a plain
 * face because one clear did not get through.
 */
export function imagePainter(send: (image: string | undefined) => Promise<void>) {
  let state: number | undefined;
  let painted: string | undefined;
  return {
    state(next: number): void {
      state = next;
    },
    async paint(image: string | undefined): Promise<void> {
      const key = `${state}|${image ?? ""}`;
      if (key === painted) return;
      painted = undefined;
      await send(image);
      painted = key;
    },
  };
}

/**
 * The key image for setImage: the SVG as a base64 data URL with its MIME type, a form Stream Deck
 * documents for setImage. Base64 rather than raw text, so the gradient's "#tile" reference can never
 * be read as a URL fragment.
 */
export function destinationImage(tone: KeyTone, mark: DestinationMark, titleLines: number): string {
  const svg = layoutSvg(destinationLayout(tone, mark, titleLines));
  return `data:image/svg+xml;base64,${Buffer.from(svg, "utf8").toString("base64")}`;
}
