import type { Theme } from "./theme";

/** --card-raised in the light theme (oklch 95.6% 0.009 265): the darkest ground
 *  a chat row is drawn on (a hovered row), so a name that reads here reads on
 *  every lighter one. Hex, because an author colour is a hex and the maths is
 *  sRGB. palette-contrast.test.ts holds it to the token. */
export const LIGHT_CHAT_GROUND = "#edf0f7";

const AA = 4.5;

const lin = (v: number) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);

function parseHex(color: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(color);
  if (!m) return null;
  const h = m[1].length === 3 ? [...m[1]].map((c) => c + c).join("") : m[1];
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
}

function luminance([r, g, b]: [number, number, number]): number {
  return 0.2126 * lin(r / 255) + 0.7152 * lin(g / 255) + 0.0722 * lin(b / 255);
}

function contrastOn(rgb: [number, number, number], ground: [number, number, number]): number {
  const [a, b] = [luminance(rgb), luminance(ground)];
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

/** The colour to paint a chat author's name in.
 *
 *  The colour is the PLATFORM's (Twitch's user colour, mostly), chosen for a
 *  dark chat window, and the console cannot ask for another. On the dark theme
 *  it is used as given. On the light theme it is darkened toward black until it
 *  reads at AA (4.5:1) on the darkest card a chat row sits on -- the hue is
 *  kept, so a yellow becomes a dark olive and not a grey, and a colour that
 *  already reads is returned untouched. Anything that is not a hex colour is
 *  returned as it came: this will not invent a colour for a value it cannot
 *  read. */
export function authorInk(color: string, theme: Theme): string {
  if (theme === "dark") return color;
  const rgb = parseHex(color);
  const ground = parseHex(LIGHT_CHAT_GROUND)!;
  if (!rgb || contrastOn(rgb, ground) >= AA) return color;

  // The least darkening that reads: bisect the blend toward black. Linear in
  // gamma-encoded sRGB, like a browser's own mix, so the hue stays put.
  let lo = 0;
  let hi = 1;
  for (let i = 0; i < 12; i++) {
    const mid = (lo + hi) / 2;
    // Rounded as it is measured: the eight-bit colour is what is painted.
    const mixed = rgb.map((v) => Math.round(v * (1 - mid))) as [number, number, number];
    if (contrastOn(mixed, ground) >= AA) hi = mid;
    else lo = mid;
  }
  const out = rgb.map((v) => Math.round(v * (1 - hi)));
  return "#" + out.map((v) => v.toString(16).padStart(2, "0")).join("");
}
