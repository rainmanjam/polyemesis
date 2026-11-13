/* What each of the eight keys draws, from the plugin's own key code.
 *
 * The destination keys are the plugin's: the tile colours, the platform mark,
 * the name and where they sit all come from `destinationLayout` in
 * integrations/streamdeck/src/keyImage.ts, and the words under the mark are the
 * plugin's own catalogue (src/locales/en.json). The faces are built as the
 * plugin's `KeyFace`, the same shape `titleFor` writes a key's title from, so
 * faces.test.ts can hold every line to the plugin's own functions.
 *
 * Only the catalogue's English file is imported: src/i18n.ts would pull in all
 * fifteen. drawKey is the one browser-only part (it needs a canvas). */
import en from "../../../../../integrations/streamdeck/src/locales/en.json";
import { FACE, destinationLayout, layoutSvg, type FaceLayout } from "../../../../../integrations/streamdeck/src/keyImage.js";
import type { KeyFace, KeyTone } from "../../../../../integrations/streamdeck/src/keyFace.js";
import type { StreamDeckKey } from "../../../../../integrations/streamdeck/src/keys.js";
import { TITLE_COLOURS } from "../../../../../integrations/streamdeck/scripts/keyPalette.mjs";
import { DESTS, type DeckState, type DestState } from "./state";

const catalogue = en as Record<string, string>;
const say = (key: StreamDeckKey, vars: Record<string, string | number> = {}): string =>
  (catalogue[key] ?? key).replace(/\{(\w+)\}/g, (whole, name: string) => (Object.hasOwn(vars, name) ? String(vars[name]) : whole));

/** A destination's state as the plugin's face: its tone, label and (for a failure) the classified cause. */
const DEST_FACE: Record<DestState, Pick<KeyFace, "tone" | "label" | "reason">> = {
  off: { tone: "idle", label: "streamdeck.state.off" },
  starting: { tone: "warn", label: "streamdeck.state.starting" },
  live: { tone: "live", label: "streamdeck.state.live" },
  failed: { tone: "error", label: "streamdeck.state.failed", reason: "streamdeck.reason.timeout" },
};

/** The preset the Routing preset key shows while it is held. Illustrative: it is the operator's own preset name. */
const PRESET_NAME = "No music";

/** Key order: Go live, Stop all, Routing preset, then the five destinations. */
export function keyFace(s: DeckState, key: number): KeyFace {
  if (key >= 3) {
    const d = DESTS[key - 3];
    return { ...DEST_FACE[s.dests[key - 3]], mark: { platform: d.platform, name: d.name } };
  }
  if (key === 0) return { tone: "idle", label: "streamdeck.go.live" };
  if (key === 1) {
    if (s.held !== "stopall") return { tone: "idle", label: "streamdeck.stop.hold" };
    const youtube = s.dests[0] === "live" ? 1 : 0;
    return {
      tone: "held",
      label: "streamdeck.stop.hold",
      detail: youtube > 0 ? say("streamdeck.stop.youtube", { count: youtube }) : say("streamdeck.stop.noYoutube"),
    };
  }
  if (s.held === "preset") return { tone: "held", label: "streamdeck.preset.applying", detail: PRESET_NAME };
  return { tone: "idle", label: "streamdeck.preset.hold" };
}

/** The lines Stream Deck draws under the key image, as the plugin's `titleFor` writes them. */
export function titleLines(face: KeyFace): string[] {
  const lines = [say(face.label, face.labelVars)];
  if (face.reason) lines.push(say(face.reason, face.reasonVars));
  if (face.detail) lines.push(face.detail);
  return lines;
}

/** The layout of a destination key's image: the plugin's, for this face's title. */
export function layoutFor(face: KeyFace): FaceLayout {
  if (!face.mark) throw new Error("layoutFor: not a destination face");
  return destinationLayout(face.tone, face.mark, titleLines(face).length);
}

/** The mark's path, from the plugin's own SVG for the layout (24-unit box). */
export function glyphPath(layout: FaceLayout): string {
  const m = /<path d="([^"]+)" fill=/.exec(layoutSvg(layout));
  return m?.[1] ?? "";
}

/** The plugin's tile image for a control key's face (imgs/keys/<action>/<tone>.png), or undefined for a destination. */
export function tileFor(face: KeyFace): string | undefined {
  const action = face.label === "streamdeck.go.live" ? "go-live" : face.label === "streamdeck.stop.hold" ? "stop-all" : face.label.startsWith("streamdeck.preset.") ? "preset" : undefined;
  return action ? `${action}/${face.tone}` : undefined;
}

/* ----------------------------------------------------------------- drawing */

const KS = 256;
const SCALE = KS / FACE;
const FONT = '"Helvetica Neue",Helvetica,Arial,sans-serif';
let tiles: Record<string, CanvasImageSource> = {};
/** The plugin's PNG tiles, keyed like tileFor's ids. Set once by the scene after they load. */
export function setTiles(images: Record<string, CanvasImageSource>): void {
  tiles = images;
}
const glyphCache = new Map<string, Path2D>();
const rgb = (c: readonly number[]) => `rgb(${c[0]},${c[1]},${c[2]})`;

function lcd(ctx: CanvasRenderingContext2D, k: number): void {
  /* A little LCD character under the glyphs: backlight falloff toward the edges. */
  const g = ctx.createRadialGradient(KS / 2, KS * 0.42, KS * 0.18, KS / 2, KS / 2, KS * 0.78);
  g.addColorStop(0, "rgba(0,0,0,0)");
  g.addColorStop(1, `rgba(0,0,0,${0.26 * k})`);
  ctx.fillStyle = g;
  ctx.fillRect(0, 0, KS, KS);
}

function fitText(ctx: CanvasRenderingContext2D, txt: string, px: number, max: number, min: number): void {
  let p = px;
  ctx.font = `600 ${p}px ${FONT}`;
  while (ctx.measureText(txt).width > max && p > min) {
    p -= 1;
    ctx.font = `600 ${p}px ${FONT}`;
  }
}

const KEY_TONES = new Set<KeyTone>(["idle", "live", "warn", "error", "held"]);

/** Draws one key's whole face (tile, mark, name, title) into a 256 px canvas. */
export function drawKey(ctx: CanvasRenderingContext2D, face: KeyFace): void {
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.fillStyle = "#08090b";
  ctx.fillRect(0, 0, KS, KS);
  const inset = 0.02 * KS;
  const clip = () => {
    ctx.beginPath();
    ctx.roundRect(inset, inset, KS - 2 * inset, KS - 2 * inset, 0.14 * KS);
  };
  const tile = tileFor(face);
  const img = tile ? tiles[tile] : undefined;
  if (face.mark) {
    const layout = layoutFor(face);
    const g = ctx.createLinearGradient(0, 0, 0, KS);
    g.addColorStop(0, rgb(layout.tileTop));
    g.addColorStop(1, rgb(layout.tileBottom));
    ctx.fillStyle = g;
    clip();
    ctx.fill();
    ctx.save();
    clip();
    ctx.clip();
    lcd(ctx, layout.tone === "idle" ? 1 : 0.7);
    ctx.restore();
    const ink = rgb(layout.ink);
    let path = glyphCache.get(layout.glyph.id + layout.tone);
    if (!path) glyphCache.set(layout.glyph.id + layout.tone, (path = new Path2D(glyphPath(layout))));
    ctx.save();
    ctx.translate(layout.glyph.x * SCALE, layout.glyph.y * SCALE);
    const k = (layout.glyph.size / 24) * SCALE;
    ctx.scale(k, k);
    ctx.fillStyle = ink;
    ctx.fill(path);
    ctx.restore();
    if (layout.name) {
      ctx.fillStyle = ink;
      ctx.font = `700 ${layout.name.fontSize * SCALE}px ${FONT}`;
      ctx.textAlign = "left";
      ctx.textBaseline = "alphabetic";
      ctx.fillText(layout.name.text, layout.name.x * SCALE, layout.name.baseline * SCALE);
    }
  } else if (img) {
    ctx.drawImage(img, 0, 0, KS, KS);
    ctx.save();
    clip();
    ctx.clip();
    lcd(ctx, 1);
    ctx.restore();
  }
  /* The title, in the state's title colour, as Stream Deck draws it at the bottom of the key. */
  const tone = KEY_TONES.has(face.tone) ? face.tone : "idle";
  const dark = tone === "warn";
  ctx.textAlign = "center";
  ctx.textBaseline = "alphabetic";
  ctx.fillStyle = rgb(TITLE_COLOURS[tone]);
  if (!dark) {
    ctx.shadowColor = "rgba(0,0,0,.55)";
    ctx.shadowBlur = 2.5 * SCALE;
    ctx.shadowOffsetY = SCALE;
  }
  const lines = titleLines(face);
  lines.forEach((t, i) => {
    fitText(ctx, t, 22 * SCALE, KS - 12 * SCALE, 13 * SCALE);
    ctx.fillText(t, KS / 2, KS - (9 + (lines.length - 1 - i) * 22 * 1.14) * SCALE - 2 * SCALE);
  });
  ctx.shadowColor = "transparent";
}
