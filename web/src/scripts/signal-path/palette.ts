/** Track colours for the WebGL layers, resolved from the site's own custom properties.
 *
 * The wires, packets and faders draw in RGB, but the colours are defined once, as tokens, in
 * tokens.css and global.css (--color-meter for the mic, --color-cohost for music, --color-game for
 * game, --color-second-voice for the co-host mic). Reading them at runtime keeps one palette: a token change reaches the canvas without a
 * second list of hex values to forget. The tokens are oklch(); a 1x1 canvas converts any CSS colour
 * the browser understands to sRGB bytes, which is what Three.js wants.
 *
 * Each entry has a fallback used only if the browser cannot parse the value, so a missing token
 * degrades to a sensible colour rather than black. */
export interface Palette {
  video: number;
  mic: number;
  music: number;
  game: number;
  cohost: number;
  spareA: number;
  groove: number;
}

const SOURCES: Record<keyof Palette, [string, number]> = {
  video: ["--sp-video", 0xc3cada],
  mic: ["--sp-mic", 0x45b5d0],
  music: ["--sp-music", 0xe879f9],
  game: ["--sp-game", 0x9b8cff],
  cohost: ["--sp-cohost", 0x0bf0e0],
  spareA: ["--sp-spare-a", 0x7d8798],
  groove: ["--sp-groove", 0x2c3444],
};

/** Parse any CSS colour to 0xRRGGBB, or null when the browser rejects it. */
export function cssColorToHex(value: string, ctx: Pick<CanvasRenderingContext2D, "fillStyle" | "fillRect" | "getImageData">): number | null {
  if (!value) return null;
  // Two different sentinels: a value the context rejects leaves fillStyle untouched.
  ctx.fillStyle = "#000001";
  ctx.fillStyle = value;
  const first = String(ctx.fillStyle);
  ctx.fillStyle = "#000002";
  ctx.fillStyle = value;
  if (first !== String(ctx.fillStyle)) return null;
  ctx.fillRect(0, 0, 1, 1);
  const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data;
  return (r << 16) | (g << 8) | b;
}

export function readPalette(root: Element): Palette {
  const styles = getComputedStyle(root);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  const palette = {} as Palette;
  for (const key of Object.keys(SOURCES) as (keyof Palette)[]) {
    const [property, fallback] = SOURCES[key];
    palette[key] = (ctx && cssColorToHex(styles.getPropertyValue(property).trim(), ctx)) ?? fallback;
  }
  return palette;
}
