// The key face palette, shared by scripts/icons.mjs (which draws the faces) and
// test/keyContrast.test.ts (which holds every face to WCAG AA for its title).
//
// THE TITLE COLOUR IS PER STATE, AND THE MANIFEST SETS IT. The plugin writes a
// key's title with setTitle, which carries text only; Stream Deck draws it in the
// title colour of the state the key is in. Every key state in manifest.json
// carries a TitleColor, and the keys switch state with setState(KEY_STATE.x), so
// the colour on the device is the one TITLE_COLOURS gives that state below. The
// actions set UserTitleEnabled: false, so the title is the plugin's alone.
// test/keyContrast.test.ts fails if a manifest TitleColor and TITLE_COLOURS disagree.
//
// White titles on the UI's signal colours at full lightness (--live, --primary)
// failed AA: those tokens are built to sit ON the dark surface, not UNDER white
// text. So live, held and restarted keep their token's HUE (and as much chroma as
// sRGB allows) from web/src/styles/tokens.css and ui/src/index.css, and drop the
// lightness until white reads at 4.5:1 everywhere on the tile.
//
// Warn is the exception, because amber darkened that far reads as brown. It keeps
// --warn as it is, bright, and its title is near-black instead: --background,
// the app shell's ink-dark. Only warn does this; every other state's title is
// white. Green is live, amber a warning, red failed, the accent blue held, grey off.

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

/** White, the title on every state but warn. */
export const TITLE_WHITE = [255, 255, 255];
/** Near-black for the title on the bright warn tile: --background in ui/src/index.css. */
export const TITLE_DARK = oklch(0.159, 0.0091, 264.3);

/** "#RRGGBB" for a colour, the form the manifest's TitleColor takes. */
export const hex = (rgb) => `#${rgb.map((v) => v.toString(16).padStart(2, "0")).join("").toUpperCase()}`;

/**
 * The tile is lit a little from the top: its colour times (SHADE_TOP - SHADE_SLOPE * y),
 * y from 0 at the top to 1 at the bottom. The lightest pixel is the worst case for a
 * white title and the darkest the worst case for a dark one, so the test reads both.
 */
export const SHADE_TOP = 1.06;
export const SHADE_SLOPE = 0.12;

/** Key face colours by state, in the order KEY_STATE in src/render.ts defines them. */
export const KEY_COLOURS = {
  idle: [58, 63, 71],
  // --live's hue (149.9), darkened from L 75.6% so a white title reads on it.
  live: oklch(0.52, 0.14, 149.9),
  // --warn itself, bright, under a near-black title (TITLE_COLOURS).
  warn: oklch(0.765, 0.1399, 72.9),
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
  // A level that reached the output by a RESTART: the viewer saw a reconnect. A dark burnt
  // orange (hue 48, redder than the warn amber's 72.9) under a WHITE title, against warn's
  // bright amber under a dark one, and its own circular-arrow glyph, so it is never read
  // as an ordinary warning.
  restarted: oklch(0.54, 0.14, 48),
  // The control cannot apply here (audio copy, a matrix mix). Grey: nothing is wrong with the stream.
  unavailable: [74, 79, 90],
};

/** The title colour each state is drawn with: white, except near-black on the bright warn tile. */
export const TITLE_COLOURS = Object.fromEntries(
  Object.keys(KEY_COLOURS).map((state) => [state, state === "warn" ? TITLE_DARK : TITLE_WHITE]),
);
