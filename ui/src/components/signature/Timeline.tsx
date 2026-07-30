import { useCallback, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { cn } from "@/lib/utils";
// The timecode and sprite functions this component draws with live in lib
// because the clip editor needs them too. See lib/timeline.ts for why they are
// not here.
import { spriteAt, timecode, type SpriteCue } from "@/lib/timeline";

/* ===========================================================================
   The clip timeline.

   Bespoke, and drawn on canvas for the same reason the meters are: a recording
   has thousands of keyframes and a DOM node per tick would make dragging an
   in-point feel like editing a spreadsheet. Every colour is read from the
   theme's CSS variables at mount, so this reads as a native part of the kit.

   The one thing this component exists to make impossible is a silently moved
   in-point. A fast cut can only start on a keyframe, so the delivered start is
   almost never the one that was asked for — and the difference is drawn: the
   keyframe ticks are visible, the snapped start has its own marker, and the
   span between what you asked for and what you will get is shaded. A tool that
   moves your cut without telling you is a tool nobody trusts twice.

   It is NOT a video editor. There is one selection, no tracks to arrange and
   nothing to drop onto it. Everything here serves "where does the clip start,
   where does it stop".
   =========================================================================== */

/** One recording file's span on the timeline. A clip may cross several. */
export interface TimelineSegmentMark {
  startMs: number;
  endMs: number;
  name: string;
  /** The master is not on disk. Drawn hatched rather than dropped: a hole you
   *  can see beats a timeline that quietly got shorter. */
  missing?: boolean;
}

export type TimelineVariant = "overview" | "detail";

export interface TimelineProps {
  variant: TimelineVariant;
  /** The window this track draws. The overview draws the whole recording; the
   *  detail track draws the zoomed span the operator is working in. */
  viewStartMs: number;
  viewEndMs: number;

  inMs: number;
  outMs: number;
  playheadMs: number;

  /** Random-access points, in timeline ms. */
  keyframes?: number[];
  /** False means nobody could read them — which is a different thing from
   *  "there are none", and is drawn differently. */
  keyframesKnown?: boolean;
  /** Where a fast cut would actually begin. Null when it lands exactly on the
   *  in-point or when the mode does not snap. */
  snapInMs?: number | null;

  segments?: TimelineSegmentMark[];
  sprites?: SpriteCue[];

  /** Overview only: the span the detail track is showing. */
  windowMs?: [number, number] | null;

  onSeek?: (ms: number) => void;
  onChangeIn?: (ms: number) => void;
  onChangeOut?: (ms: number) => void;
  /** Overview only: the operator dragged the view window somewhere else. */
  onMoveWindow?: (startMs: number) => void;

  className?: string;
}

const OVERVIEW_HEIGHT = 34;
const DETAIL_HEIGHT = 72;

/** How close to a handle counts as grabbing it. Generous: an in-point that
 *  needs a pixel-perfect click is an in-point nobody drags twice. */
const GRAB_PX = 9;

/** Below this spacing the keyframe ticks stop being individual marks and start
 *  being a smear, so they are drawn as a density band instead. Drawing 4000
 *  one-pixel lines over 900px is not information, it is a grey rectangle. */
const MIN_TICK_GAP_PX = 3;

// ------------------------------------------------------------------- palette

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

interface Palette {
  bg: string;
  grid: string;
  border: string;
  primary: string;
  primaryDim: string;
  playhead: string;
  warn: string;
  armed: string;
  muted: string;
  text: string;
}

// index.css is authoritative; these literals only exist because a canvas
// fillStyle cannot be the empty string if the stylesheet has not applied yet.
function readPalette(): Palette {
  return {
    bg: cssVar("--meter-bg", "#12161e"),
    grid: cssVar("--meter-grid", "#232a38"),
    border: cssVar("--border-strong", "#303a4d"),
    primary: cssVar("--primary", "#5b7fc7"),
    primaryDim: cssVar("--primary-dim", "#253148"),
    playhead: cssVar("--meter-hold", "#e6e9ef"),
    warn: cssVar("--warn", "#e8a33d"),
    armed: cssVar("--armed", "#45b5d0"),
    muted: cssVar("--subtle-foreground", "#5d6779"),
    text: cssVar("--muted-foreground", "#8b94a7"),
  };
}

// -------------------------------------------------------------------- ticks

/** A ruler step that lands on a round number of seconds or minutes. Ticks at
 *  "every 7.3 s" are a ruler nobody can read a position off. */
const RULER_STEPS_MS = [
  100, 250, 500, 1000, 2000, 5000, 10000, 15000, 30000, 60000, 120000, 300000, 600000,
  900000, 1800000, 3600000,
];

function rulerStep(spanMs: number, widthPx: number): number {
  const target = (spanMs / Math.max(widthPx, 1)) * 90; // ~90px between labels
  for (const step of RULER_STEPS_MS) {
    if (step >= target) return step;
  }
  return RULER_STEPS_MS[RULER_STEPS_MS.length - 1];
}

/** Ruler labels drop the milliseconds and the hour when there is not one. */
function rulerLabel(ms: number, spanMs: number): string {
  const t = Math.max(0, Math.round(ms));
  const h = Math.floor(t / 3600000);
  const m = Math.floor((t % 3600000) / 60000);
  const s = Math.floor((t % 60000) / 1000);
  const head = h > 0 ? `${h}:${String(m).padStart(2, "0")}` : `${m}`;
  if (spanMs < 10000) {
    return `${head}:${String(s).padStart(2, "0")}.${String(Math.floor((t % 1000) / 100))}`;
  }
  return `${head}:${String(s).padStart(2, "0")}`;
}

// ----------------------------------------------------------------- component

type DragKind = "in" | "out" | "seek" | "window" | null;

export function Timeline({
  variant,
  viewStartMs,
  viewEndMs,
  inMs,
  outMs,
  playheadMs,
  keyframes,
  keyframesKnown = true,
  snapInMs = null,
  segments,
  sprites,
  windowMs = null,
  onSeek,
  onChangeIn,
  onChangeOut,
  onMoveWindow,
  className,
}: TimelineProps) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<number | null>(null);
  const dragRef = useRef<DragKind>(null);
  const dragOffsetRef = useRef(0);

  const detail = variant === "detail";
  const height = detail ? DETAIL_HEIGHT : OVERVIEW_HEIGHT;
  const span = Math.max(1, viewEndMs - viewStartMs);

  const toPx = useCallback(
    (ms: number) => ((ms - viewStartMs) / span) * width,
    [viewStartMs, span, width],
  );
  const toMs = useCallback(
    (px: number) => viewStartMs + (Math.max(0, Math.min(width, px)) / Math.max(width, 1)) * span,
    [viewStartMs, span, width],
  );

  // Width comes from the element rather than from a prop so the timeline is as
  // wide as the panel it is in, at whatever the window happens to be.
  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || width <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    draw(ctx, {
      width,
      height,
      detail,
      viewStartMs,
      span,
      inMs,
      outMs,
      playheadMs,
      keyframes,
      keyframesKnown,
      snapInMs,
      segments,
      windowMs,
    });
  }, [
    width,
    height,
    detail,
    viewStartMs,
    span,
    inMs,
    outMs,
    playheadMs,
    keyframes,
    keyframesKnown,
    snapInMs,
    segments,
    windowMs,
  ]);

  const hitTest = useCallback(
    (px: number): DragKind => {
      if (onChangeIn && Math.abs(px - toPx(inMs)) <= GRAB_PX) return "in";
      if (onChangeOut && Math.abs(px - toPx(outMs)) <= GRAB_PX) return "out";
      if (variant === "overview" && windowMs && onMoveWindow) {
        const a = toPx(windowMs[0]);
        const b = toPx(windowMs[1]);
        if (px >= a && px <= b) return "window";
      }
      return "seek";
    },
    [onChangeIn, onChangeOut, onMoveWindow, toPx, inMs, outMs, variant, windowMs],
  );

  const localX = (e: { clientX: number }) => {
    const rect = wrapRef.current?.getBoundingClientRect();
    return rect ? e.clientX - rect.left : 0;
  };

  const apply = useCallback(
    (kind: DragKind, px: number) => {
      const ms = toMs(px);
      switch (kind) {
        case "in":
          // Clamped against the other handle here rather than in the page, so
          // dragging past it parks the handle instead of inverting the cut.
          onChangeIn?.(Math.min(ms, outMs - 1));
          break;
        case "out":
          onChangeOut?.(Math.max(ms, inMs + 1));
          break;
        case "window":
          onMoveWindow?.(toMs(px - dragOffsetRef.current));
          break;
        case "seek":
          onSeek?.(ms);
          break;
        default:
          break;
      }
    },
    [toMs, onChangeIn, onChangeOut, onMoveWindow, onSeek, inMs, outMs],
  );

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || width <= 0) return;
    const px = localX(e);
    const kind = hitTest(px);
    dragRef.current = kind;
    if (kind === "window" && windowMs) {
      // Grab the window where it was picked up, so it does not jump its own
      // width the moment the pointer moves.
      dragOffsetRef.current = px - toPx(windowMs[0]);
    }
    (e.target as Element).setPointerCapture?.(e.pointerId);
    apply(kind, px);
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const px = localX(e);
    setHover(px >= 0 && px <= width ? toMs(px) : null);
    if (dragRef.current) apply(dragRef.current, px);
  };

  const endDrag = () => {
    dragRef.current = null;
  };

  const cursor =
    variant === "overview" && windowMs
      ? "grab"
      : dragRef.current === "in" || dragRef.current === "out"
        ? "ew-resize"
        : "pointer";

  const hoverSprite = detail && hover !== null ? spriteAt(sprites, hover) : null;

  return (
    <div className={cn("relative select-none", className)}>
      <div
        ref={wrapRef}
        className="relative w-full touch-none rounded-[3px] border border-border bg-meter-bg"
        style={{ height, cursor }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onPointerLeave={() => {
          setHover(null);
          endDrag();
        }}
      >
        <canvas ref={canvasRef} className="block h-full w-full" />

        {/* The handles are real focusable elements on top of the canvas: the
            drawing is for the eye, but an in-point has to be reachable from
            the keyboard, and a screen reader has to be able to say where it
            is. Pointer events pass through to the canvas wrapper, which owns
            dragging. */}
        {onChangeIn && (
          <Handle
            kind="in"
            label="Clip in-point"
            valueMs={inMs}
            leftPx={toPx(inMs)}
            width={width}
            onNudge={(d) => onChangeIn(Math.min(inMs + d, outMs - 1))}
          />
        )}
        {onChangeOut && (
          <Handle
            kind="out"
            label="Clip out-point"
            valueMs={outMs}
            leftPx={toPx(outMs)}
            width={width}
            onNudge={(d) => onChangeOut(Math.max(outMs + d, inMs + 1))}
          />
        )}
      </div>

      {/* Hover readout. The thumbnail is the sprite sheet the thumbnail job
          wrote; without one the timecode alone still tells you where you are. */}
      {detail && hover !== null && (
        <div
          className="pointer-events-none absolute z-20 -translate-x-1/2"
          style={{
            left: Math.max(48, Math.min(width - 48, toPx(hover))),
            bottom: height + 6,
          }}
        >
          {hoverSprite && (
            <div
              className="mb-1 rounded-[3px] border border-border-strong bg-meter-bg bg-no-repeat shadow-lg"
              style={{
                width: hoverSprite.w,
                height: hoverSprite.h,
                backgroundImage: `url("${hoverSprite.url}")`,
                backgroundPosition: `-${hoverSprite.x}px -${hoverSprite.y}px`,
              }}
            />
          )}
          <div className="tnum rounded-[3px] bg-popover px-1 py-0.5 text-center font-mono text-[10px] text-foreground shadow">
            {timecode(hover)}
          </div>
        </div>
      )}
    </div>
  );
}

/** One draggable marker. Positioned by the canvas's own arithmetic so it can
 *  never drift from the line drawn under it. */
function Handle({
  kind,
  label,
  valueMs,
  leftPx,
  width,
  onNudge,
}: {
  kind: "in" | "out";
  label: string;
  valueMs: number;
  leftPx: number;
  width: number;
  onNudge: (deltaMs: number) => void;
}) {
  if (leftPx < -20 || leftPx > width + 20) return null;
  return (
    <div
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuenow={Math.round(valueMs)}
      aria-valuetext={timecode(valueMs)}
      title={`${label}: ${timecode(valueMs)}`}
      onKeyDown={(e) => {
        // Shift is the coarse step. The fine step is a frame at 30 fps: the
        // recording's real rate is not indexed, so this is named in
        // milliseconds in the editor rather than pretending to know it.
        const step = e.shiftKey ? 1000 : 33;
        if (e.key === "ArrowLeft") {
          e.preventDefault();
          onNudge(-step);
        } else if (e.key === "ArrowRight") {
          e.preventDefault();
          onNudge(step);
        }
      }}
      className={cn(
        "absolute top-0 z-10 h-full w-[11px] -translate-x-1/2 cursor-ew-resize",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring",
      )}
      style={{ left: leftPx }}
    >
      {/* The grip: a flag at the top so the two ends are told apart at a
          glance even when the selection is only a few pixels wide. */}
      <div
        className={cn(
          "absolute top-0 h-2.5 w-2 bg-primary",
          kind === "in" ? "left-1/2 rounded-tr-[2px]" : "right-1/2 rounded-tl-[2px]",
        )}
      />
    </div>
  );
}

// ------------------------------------------------------------------ drawing

interface DrawState {
  width: number;
  height: number;
  detail: boolean;
  viewStartMs: number;
  span: number;
  inMs: number;
  outMs: number;
  playheadMs: number;
  keyframes?: number[];
  keyframesKnown: boolean;
  snapInMs: number | null;
  segments?: TimelineSegmentMark[];
  windowMs: [number, number] | null;
}

function draw(ctx: CanvasRenderingContext2D, st: DrawState) {
  const p = readPalette();
  const { width: w, height: h } = st;
  const x = (ms: number) => ((ms - st.viewStartMs) / st.span) * w;

  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = p.bg;
  ctx.fillRect(0, 0, w, h);

  drawSegments(ctx, st, p, x);
  drawSelection(ctx, st, p, x);
  drawKeyframes(ctx, st, p, x);
  drawRuler(ctx, st, p, x);
  drawWindow(ctx, st, p, x);
  drawPlayhead(ctx, st, p, x);
}

/** Segment boundaries: where one recording file ends and the next begins. A
 *  cut that crosses one is joined before it is trimmed, which is worth being
 *  able to see before pressing export. */
function drawSegments(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  if (!st.segments) return;
  for (let i = 0; i < st.segments.length; i++) {
    const seg = st.segments[i];
    const a = x(seg.startMs);
    const b = x(seg.endMs);
    if (b < 0 || a > st.width) continue;
    if (seg.missing) {
      // Hatched, because "this part of the recording is not on disk" must not
      // look like ordinary empty timeline.
      ctx.save();
      ctx.beginPath();
      ctx.rect(a, 0, b - a, st.height);
      ctx.clip();
      ctx.strokeStyle = p.muted;
      ctx.lineWidth = 1;
      for (let hx = a - st.height; hx < b + st.height; hx += 7) {
        ctx.beginPath();
        ctx.moveTo(hx, st.height);
        ctx.lineTo(hx + st.height, 0);
        ctx.stroke();
      }
      ctx.restore();
    } else if (i % 2 === 1) {
      // Alternating bands, faint: they say "another file" without competing
      // with the selection for attention.
      ctx.fillStyle = p.grid;
      ctx.globalAlpha = 0.35;
      ctx.fillRect(a, 0, b - a, st.height);
      ctx.globalAlpha = 1;
    }
    if (i > 0) {
      ctx.strokeStyle = p.border;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(Math.round(a) + 0.5, 0);
      ctx.lineTo(Math.round(a) + 0.5, st.height);
      ctx.stroke();
    }
  }
}

/** The selection, and — the part that matters — the drift between the
 *  requested in-point and the keyframe a fast cut will really start on. */
function drawSelection(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  const a = x(st.inMs);
  const b = x(st.outMs);
  ctx.fillStyle = p.primaryDim;
  ctx.globalAlpha = 0.75;
  ctx.fillRect(a, 0, Math.max(1, b - a), st.height);
  ctx.globalAlpha = 1;

  if (st.snapInMs !== null && Math.abs(st.snapInMs - st.inMs) > 1) {
    // The shaded span is footage the clip will contain that nobody asked for.
    // Drawn in the warning colour and drawn ON TOP of the selection, because
    // this is the single fact a user is most likely to be surprised by.
    const s = x(st.snapInMs);
    ctx.fillStyle = p.warn;
    ctx.globalAlpha = 0.22;
    ctx.fillRect(Math.min(s, a), 0, Math.abs(a - s), st.height);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = p.warn;
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 2]);
    ctx.beginPath();
    ctx.moveTo(Math.round(s) + 0.5, 0);
    ctx.lineTo(Math.round(s) + 0.5, st.height);
    ctx.stroke();
    ctx.setLineDash([]);
  }

  ctx.strokeStyle = p.primary;
  ctx.lineWidth = 1;
  for (const px of [a, b]) {
    ctx.beginPath();
    ctx.moveTo(Math.round(px) + 0.5, 0);
    ctx.lineTo(Math.round(px) + 0.5, st.height);
    ctx.stroke();
  }
}

/** Keyframe ticks. Only on the detail track: at an hour across 900 pixels they
 *  would be a solid bar and would say nothing. */
function drawKeyframes(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  if (!st.detail) return;
  if (!st.keyframesKnown) {
    // A dashed baseline, not an empty one. "Nobody could read the keyframes"
    // and "there are no keyframes here" would otherwise look identical, and
    // only one of them means the in-point may move without being measured.
    ctx.strokeStyle = p.muted;
    ctx.lineWidth = 1;
    ctx.setLineDash([2, 3]);
    ctx.beginPath();
    ctx.moveTo(0, st.height - 5.5);
    ctx.lineTo(st.width, st.height - 5.5);
    ctx.stroke();
    ctx.setLineDash([]);
    return;
  }
  if (!st.keyframes || st.keyframes.length === 0) return;
  const top = st.height - 10;
  const visible = st.keyframes.filter((k) => k >= st.viewStartMs && k <= st.viewStartMs + st.span);
  const gap = visible.length > 1 ? st.width / visible.length : st.width;
  if (gap < MIN_TICK_GAP_PX) {
    // Too dense to read as marks. A band says "keyframes everywhere here"
    // without pretending each smear is one.
    ctx.fillStyle = p.armed;
    ctx.globalAlpha = 0.3;
    ctx.fillRect(0, top, st.width, 8);
    ctx.globalAlpha = 1;
    return;
  }
  ctx.strokeStyle = p.armed;
  ctx.lineWidth = 1;
  for (const k of visible) {
    const px = Math.round(x(k)) + 0.5;
    ctx.beginPath();
    ctx.moveTo(px, top);
    ctx.lineTo(px, st.height);
    ctx.stroke();
  }
}

function drawRuler(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  const step = rulerStep(st.span, st.width);
  const first = Math.ceil(st.viewStartMs / step) * step;
  ctx.strokeStyle = p.grid;
  ctx.fillStyle = p.text;
  ctx.font = "9px ui-monospace, SFMono-Regular, monospace";
  ctx.textBaseline = "top";
  for (let ms = first; ms <= st.viewStartMs + st.span; ms += step) {
    const px = Math.round(x(ms)) + 0.5;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(px, 0);
    ctx.lineTo(px, st.detail ? 6 : 4);
    ctx.stroke();
    if (st.detail) ctx.fillText(rulerLabel(ms, st.span), px + 2, 1);
  }
}

/** Overview only: the span the detail track is showing, as a draggable box. */
function drawWindow(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  if (!st.windowMs) return;
  const a = x(st.windowMs[0]);
  const b = x(st.windowMs[1]);
  ctx.strokeStyle = p.playhead;
  ctx.globalAlpha = 0.55;
  ctx.lineWidth = 1;
  ctx.strokeRect(Math.round(a) + 0.5, 0.5, Math.max(2, b - a), st.height - 1);
  ctx.globalAlpha = 1;
}

function drawPlayhead(
  ctx: CanvasRenderingContext2D,
  st: DrawState,
  p: Palette,
  x: (ms: number) => number,
) {
  const px = Math.round(x(st.playheadMs)) + 0.5;
  if (px < 0 || px > st.width) return;
  ctx.strokeStyle = p.playhead;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(px, 0);
  ctx.lineTo(px, st.height);
  ctx.stroke();
  ctx.fillStyle = p.playhead;
  ctx.beginPath();
  ctx.moveTo(px - 4, 0);
  ctx.lineTo(px + 4, 0);
  ctx.lineTo(px, 5);
  ctx.closePath();
  ctx.fill();
}
