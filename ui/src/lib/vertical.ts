/* Vertical shorts and highlight suggestions (Wave 4 #5): the wire types and
   the pure geometry the editor needs. The server is the authority on every
   number here -- see internal/clipper/vertical.go -- and these helpers only
   keep what the operator draws in a shape the server will use as drawn. */

export type VerticalLayout = "center" | "stacked" | "blur";

/** A box on the SOURCE frame, every field a fraction of its width or height. */
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface CaptionStyle {
  font?: string;
  sizePct?: number;
  color?: string;
  outlineColor?: string;
  outline?: number;
  box?: boolean;
  boxColor?: string;
  boxOpacity?: number;
  positionPct?: number;
  uppercase?: boolean;
  maxLines?: number;
  maxWords?: number;
}

/** Everything the editor holds about a short, whether or not the layout uses it. */
export interface VerticalSpec {
  layout: VerticalLayout;
  focus: number;
  face: Rect;
  game?: Rect;
  faceShare: number;
  faceBottom: boolean;
  captions: { enabled: boolean; style: CaptionStyle };
}

/** What /plan and /export take, and what a template stores. */
export interface VerticalBody {
  layout: VerticalLayout;
  focus?: number;
  face?: Rect;
  game?: Rect;
  faceShare?: number;
  faceBottom?: boolean;
  captions?: { enabled: boolean; style: CaptionStyle };
}

export interface ClipTemplate {
  id: number;
  name: string;
  vertical: VerticalBody;
  updatedAt: string;
}

export interface ClipTemplatesView {
  templates: ClipTemplate[];
  layouts: VerticalLayout[];
  fonts: string[];
  /** Whether this FFmpeg has the libass subtitles filter that burns captions. */
  burnCaptions: boolean;
  maxShortSeconds: number;
  defaultStyle: CaptionStyle;
}

export type HighlightSignalKind = "chat" | "loudness" | "model";

export interface HighlightSignal {
  kind: HighlightSignalKind;
  startMs: number;
  endMs: number;
  strength: number;
  peak: number;
  baseline: number;
}

export type HighlightState = "pending" | "accepted" | "dismissed";

export interface Highlight {
  id: number;
  recordingId: number;
  startMs: number;
  endMs: number;
  score: number;
  signalScore: number;
  modelScore?: number;
  signals: HighlightSignal[];
  excerpt: string;
  state: HighlightState;
  jobId?: number;
  createdAt: string;
  decidedAt?: string;
}

export interface HighlightsView {
  suggestions: Highlight[];
  modelAvailable: boolean;
  modelReason?: string;
}

export const OUTPUT_WIDTH = 1080;
export const OUTPUT_HEIGHT = 1920;
export const DEFAULT_FACE_SHARE = 0.4;
const MIN_SIDE = 0.02;

export function defaultVerticalSpec(): VerticalSpec {
  return {
    layout: "center",
    focus: 0.5,
    face: { x: 0.7, y: 0.05, w: 0.25, h: 0.25 },
    faceShare: DEFAULT_FACE_SHARE,
    faceBottom: false,
    captions: { enabled: false, style: {} },
  };
}

/** The face panel's width over its height, as the server cuts it. */
export function faceAspect(share: number): number {
  const h = Math.round(OUTPUT_HEIGHT * (share || DEFAULT_FACE_SHARE)) & ~1;
  return OUTPUT_WIDTH / h;
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** Gives a box the face panel's shape in SOURCE pixels -- srcAspect is the
 *  frame's width over its height -- keeping its top-left and width, and
 *  shrinking it if that would spill off the frame. */
export function lockFaceBox(r: Rect, srcAspect: number, share: number): Rect {
  const aspect = faceAspect(share);
  const x = clamp(r.x, 0, 1 - MIN_SIDE);
  const y = clamp(r.y, 0, 1 - MIN_SIDE);
  let w = clamp(r.w, MIN_SIDE, 1 - x);
  let h = (w * srcAspect) / aspect;
  if (y + h > 1) {
    h = 1 - y;
    w = (h * aspect) / srcAspect;
  }
  return { x, y, w, h };
}

/** The box a drag from one corner to the other draws, locked to the panel. */
export function boxFromDrag(a: { x: number; y: number }, b: { x: number; y: number }, srcAspect: number, share: number): Rect {
  const x = clamp(Math.min(a.x, b.x), 0, 1);
  const y = clamp(Math.min(a.y, b.y), 0, 1);
  const w = Math.max(MIN_SIDE, Math.abs(a.x - b.x));
  return lockFaceBox({ x, y, w, h: 0 }, srcAspect, share);
}

/** Moves a box a percent of the frame for an arrow key, inside the frame. */
export function nudgeBox(r: Rect, key: string): Rect {
  const step = 0.01;
  switch (key) {
    case "ArrowLeft":
      return { ...r, x: clamp(r.x - step, 0, 1 - r.w) };
    case "ArrowRight":
      return { ...r, x: clamp(r.x + step, 0, 1 - r.w) };
    case "ArrowUp":
      return { ...r, y: clamp(r.y - step, 0, 1 - r.h) };
    case "ArrowDown":
      return { ...r, y: clamp(r.y + step, 0, 1 - r.h) };
    default:
      return r;
  }
}

/** Whether a short of this length is past what the platforms call a short. A
 *  warning, never a block: the server exports it either way. */
export function overShortLimit(durationMs: number, maxShortSeconds: number | undefined): boolean {
  if (!maxShortSeconds) return false;
  return durationMs > maxShortSeconds * 1000;
}

/** The body a spec sends: only the fields its layout uses, so a template saved
 *  from a centre crop does not carry a stale face box. */
export function verticalBody(spec: VerticalSpec): VerticalBody {
  const body: VerticalBody = { layout: spec.layout };
  if (spec.layout === "center") body.focus = spec.focus;
  if (spec.layout === "stacked") {
    body.face = spec.face;
    if (spec.game) body.game = spec.game;
    body.faceShare = spec.faceShare;
    body.faceBottom = spec.faceBottom;
  }
  if (spec.captions.enabled) body.captions = { enabled: true, style: spec.captions.style };
  return body;
}

/** A template's body back into the editor's spec, defaults filled in. */
export function specFromBody(body: VerticalBody, current: VerticalSpec): VerticalSpec {
  return {
    ...current,
    layout: body.layout,
    focus: body.focus ?? current.focus,
    face: body.face ?? current.face,
    game: body.game,
    faceShare: body.faceShare ?? current.faceShare,
    faceBottom: body.faceBottom ?? false,
    captions: body.captions ? { enabled: body.captions.enabled, style: { ...body.captions.style } } : { enabled: false, style: current.captions.style },
  };
}
