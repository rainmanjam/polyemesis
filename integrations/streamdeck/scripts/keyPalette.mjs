// The key face palette, shared by scripts/icons.mjs (which draws the faces) and
// test/keyContrast.test.ts (which holds every face to WCAG AA for its title).
//
// THE TITLE IS WHITE. Stream Deck draws a key's title in the user's title colour,
// white by default, and the manifest sets no TitleColor, so every state tile has
// to carry white text. The tiles used to take the UI's signal colours at full
// lightness (--live, --warn, --primary), which are built to sit ON the dark
// surface, not UNDER white text: the live green measured 3.15:1, the warn amber
// 2.14:1, held 3.60:1 and restarted 3.89:1 at the lit top of the tile.
//
// So each signal tile keeps its token's HUE (and as much chroma as sRGB allows)
// from web/src/styles/tokens.css and ui/src/index.css, and drops the lightness
// until white reads at 4.5:1 or better everywhere on the tile. Green is still
// live, amber still a warning, red still failed, the accent blue still held,
// grey still off. test/keyContrast.test.ts fails any tile below 4.5:1.

/** OKLCH to linear sRGB, unclamped, so a caller can tell an out-of-gamut colour. */
function oklchToLinear(L, C, hDeg) {
  const h = (hDeg * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}

/** OKLCH to sRGB 0..255, the colour space the UI's CSS variables are written in. */
export function oklch(L, C, hDeg) {
  const gamma = (x) => {
    const c = Math.min(1, Math.max(0, x));
    return c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
  };
  return oklchToLinear(L, C, hDeg).map((x) => Math.round(gamma(x) * 255));
}

/** WCAG 2.x relative luminance of an sRGB colour. */
export function luminance([r, g, b]) {
  const lin = (v) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/** WCAG 2.x contrast ratio between two sRGB colours. */
export function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** The key title colour: Stream Deck's default, since the manifest sets no TitleColor. */
export const TITLE_INK = [255, 255, 255];

/**
 * The tile is lit a little from the top: its colour times (SHADE_TOP - SHADE_SLOPE * y),
 * y from 0 at the top to 1 at the bottom. The lightest pixel is the worst case for
 * white text, so the contrast test reads it there.
 */
export const SHADE_TOP = 1.06;
export const SHADE_SLOPE = 0.12;

/** Key face colours by state, in the order KEY_STATE in src/render.ts defines them. */
export const KEY_COLOURS = {
  idle: [58, 63, 71],
  // --live's hue (149.9), darkened from L 75.6% so a white title reads on it.
  live: oklch(0.52, 0.14, 149.9),
  // --warn's hue (72.9), darkened from L 76.5%. Amber at this lightness is an ochre;
  // it is still the warning hue, and white on it now reads.
  warn: oklch(0.54, 0.115, 72.9),
  error: [197, 48, 48],
  // Held is the confirm-in-progress face: --primary's hue (263.2), darkened from L 60.2%.
  held: oklch(0.54, 0.115, 263.2),
  unbound: [45, 55, 72],
  // HALTED: DISK. Crimson, not the error red, so it is told apart at a glance from "unreachable".
  halted: [150, 36, 84],
  // A write key on a read token. Muted violet: not a fault, not a state of the stream.
  needsAdmin: [86, 74, 128],
  // A muted track. The error red, because a muted track is silent on air; the crossed speaker
  // (not the action glyph) is what tells it from "unreachable" at a glance.
  muted: [197, 48, 48],
  // A level that reached the output by a RESTART: the viewer saw a reconnect. Burnt orange
  // (hue 48, redder than the warn amber's 72.9), its own colour and its own circular-arrow
  // glyph, so it is never read as an ordinary warning.
  restarted: oklch(0.54, 0.14, 48),
  // The control cannot apply here (audio copy, a matrix mix). Grey: nothing is wrong with the stream.
  unavailable: [74, 79, 90],
};
