import { useEffect, useRef } from "react";
import { cn } from "@/lib/utils";

/* ===========================================================================
   Bespoke broadcast level meters.

   Drawn on canvas because at 10 Hz across up to 36 channels, DOM updates
   would thrash layout. Every colour is read from the theme's CSS variables at
   mount, so these read as native parts of the kit and a palette change here
   needs no code change.

   Behaviour follows a hardware meter, not a progress bar:
   - Instantaneous RMS as the solid bar (what you hear).
   - Peak as a held tick that falls back slowly (what clips you).
   - The classic green -> yellow -> amber -> red gradient, with the colour
     breaks at broadcast-meaningful levels rather than at even intervals.

   THE GRADIENT IS NOT ALLOWED TO BE THE ONLY CHANNEL. "Am I too hot?" was
   answerable here only by reading a hue, which is the one question on this
   screen a deuteranopic operator, or anybody on a washed-out laptop panel in
   daylight, could not answer at all. Three redundant channels now carry the
   same reading, and each works with the colour removed entirely:
     - POSITION: the zone boundaries are drawn ON TOP of the bar, so "past the
       second mark" is a fact about geometry rather than about amber.
     - TEXTURE: everything above -6 dBFS is hatched, so the hot part of the bar
       is a different surface, not a different colour.
     - TEXT: a clip raises a literal "CLIP" flag over the meter.
   =========================================================================== */

/** Meters span -60 dBFS to 0. Below -60 is inaudible in any practical mix. */
const MIN_DB = -60;
const MAX_DB = 0;

/** Peak hold, then fall. A hold that never decays becomes a high-water mark
 *  for the session and stops being useful within a minute. */
const PEAK_HOLD_MS = 900;
const PEAK_FALL_DB_PER_SEC = 24;

/** Anything at or above this is treated as clipping and latches the indicator. */
const CLIP_DB = -0.2;
const CLIP_LATCH_MS = 1500;

function dbToFraction(db: number): number {
  if (db <= MIN_DB) return 0;
  if (db >= MAX_DB) return 1;
  return (db - MIN_DB) / (MAX_DB - MIN_DB);
}

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

interface Palette {
  low: string;
  mid: string;
  high: string;
  peak: string;
  bg: string;
  grid: string;
  hold: string;
}

// The canvas cannot use Tailwind classes, so the meter reads its colours from
// the same CSS variables the rest of the kit uses. The literals below are
// last-resort fallbacks for the case where the stylesheet has not applied yet
// (canvas needs a valid fillStyle; "" would throw). They are NOT a second
// palette: index.css is authoritative, and changing a colour there changes the
// meters without touching this file.
function readPalette(): Palette {
  return {
    low: cssVar("--meter-low", "#3ecf6d"),
    mid: cssVar("--meter-mid", "#cfd23e"),
    high: cssVar("--meter-high", "#e8a33d"),
    peak: cssVar("--meter-peak", "#e5484d"),
    bg: cssVar("--meter-bg", "#12161e"),
    grid: cssVar("--meter-grid", "#232a38"),
    hold: cssVar("--meter-hold", "#e6e9ef"),
  };
}

/** The dB levels where the gradient changes meaning. Declared once and used
 *  twice — by buildGradient below and by the boundary marks drawn over the
 *  bar — so the mark an operator reads position against can never drift away
 *  from the colour break it is standing in for. */
const ZONE_BREAKS = [-18, -12, -6, -2];

/** Where the bar starts being hot enough to hatch. Same level as the amber
 *  break, for the same reason: below it a broadcast is fine. */
const HOT_DB = -6;

/** Colour breaks at levels a mixer cares about, not at even fractions. */
function buildGradient(ctx: CanvasRenderingContext2D, w: number, p: Palette) {
  const [quiet, mid, hot, clip] = ZONE_BREAKS;
  const g = ctx.createLinearGradient(0, 0, w, 0);
  g.addColorStop(0, p.low);
  g.addColorStop(dbToFraction(quiet), p.low);
  g.addColorStop(dbToFraction(mid), p.mid);
  g.addColorStop(dbToFraction(hot), p.high);
  g.addColorStop(dbToFraction(clip), p.peak);
  g.addColorStop(1, p.peak);
  return g;
}

/** Diagonal hatch over the hot end of a bar, clipped to the part of it that is
 *  actually lit. Drawn in the trough colour rather than in a new one: this is
 *  the bar being cut away, not a fifth signal hue, and the 10/SIGNAL cap in
 *  index.css is a cap on hues rather than on ways of drawing. */
function hatchHot(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  p: Palette,
) {
  if (w <= 0) return;
  ctx.save();
  ctx.beginPath();
  ctx.rect(x, y, w, h);
  ctx.clip();
  ctx.strokeStyle = p.bg;
  ctx.globalAlpha = 0.65;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  // Step by 4px and start a bar-height back, so the leading edge of the hatch
  // does not depend on where the hot zone happens to begin.
  for (let sx = x - h; sx < x + w; sx += 4) {
    ctx.moveTo(sx, y + h);
    ctx.lineTo(sx + h, y);
  }
  ctx.stroke();
  ctx.restore();
}

export interface AudioMeterProps {
  /** Per-channel RMS in dBFS. */
  rms: number[];
  /** Per-channel peak in dBFS. */
  peak: number[];
  /** Channel labels, e.g. ["L","R"] or 5.1 names. */
  labels?: string[];
  /** Height of one channel bar, in CSS pixels. */
  barHeight?: number;
  /** Gap between bars. */
  barGap?: number;
  className?: string;
}

/** "has never clipped", and NOT 0. clipAt is compared against
 *  performance.now(), which counts from page load, so a zero sentinel means
 *  "clipped at navigation start" — and CLIP_LATCH_MS is 1.5s, which is inside
 *  the window where these meters usually mount. Every channel raised a CLIP
 *  flag for the first second and a half of the page's life, on silence. */
const NEVER = Number.NEGATIVE_INFINITY;

interface ChannelState {
  holdDb: number;
  holdSetAt: number;
  clipAt: number;
}

export function AudioMeter({
  rms,
  peak,
  labels,
  barHeight = 10,
  barGap = 3,
  className,
}: AudioMeterProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const clipRef = useRef<HTMLSpanElement>(null);
  // Latest values live in a ref so the animation loop never re-subscribes and
  // React re-renders do not restart the decay.
  const dataRef = useRef({ rms, peak });
  dataRef.current = { rms, peak };
  const stateRef = useRef<ChannelState[]>([]);
  const paletteRef = useRef<Palette | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    paletteRef.current ??= readPalette();
    let raf = 0;
    let last = performance.now();

    const draw = (now: number) => {
      const dt = Math.min((now - last) / 1000, 0.25);
      last = now;

      const p = paletteRef.current!;
      let clipping = false;
      const channels = dataRef.current.rms.length;
      const dpr = window.devicePixelRatio || 1;

      const cssHeight = channels * barHeight + Math.max(0, channels - 1) * barGap;
      const cssWidth = canvas.clientWidth || 200;

      if (canvas.width !== Math.round(cssWidth * dpr) || canvas.height !== Math.round(cssHeight * dpr)) {
        canvas.width = Math.round(cssWidth * dpr);
        canvas.height = Math.round(cssHeight * dpr);
        canvas.style.height = `${cssHeight}px`;
      }

      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, cssWidth, cssHeight);

      while (stateRef.current.length < channels) {
        stateRef.current.push({ holdDb: MIN_DB, holdSetAt: 0, clipAt: NEVER });
      }

      const gradient = buildGradient(ctx, cssWidth, p);

      for (let ch = 0; ch < channels; ch++) {
        const y = ch * (barHeight + barGap);
        const rmsDb = dataRef.current.rms[ch] ?? MIN_DB;
        const peakDb = dataRef.current.peak[ch] ?? MIN_DB;
        const st = stateRef.current[ch];

        // Peak hold: jump up instantly, fall back on a timer.
        if (peakDb >= st.holdDb) {
          st.holdDb = peakDb;
          st.holdSetAt = now;
        } else if (now - st.holdSetAt > PEAK_HOLD_MS) {
          st.holdDb = Math.max(MIN_DB, st.holdDb - PEAK_FALL_DB_PER_SEC * dt);
        }
        if (peakDb >= CLIP_DB) st.clipAt = now;

        // trough
        ctx.fillStyle = p.bg;
        ctx.fillRect(0, y, cssWidth, barHeight);

        // scale ticks, drawn under the bar so a hot signal covers them
        ctx.fillStyle = p.grid;
        for (const mark of [-50, -40, -30, -20, -12, -6, -3]) {
          const x = Math.round(dbToFraction(mark) * cssWidth);
          ctx.fillRect(x, y, 1, barHeight);
        }

        // RMS bar
        const w = dbToFraction(rmsDb) * cssWidth;
        if (w > 0) {
          ctx.fillStyle = gradient;
          ctx.fillRect(0, y, w, barHeight);
          // TEXTURE, above the amber break. The hot end of the bar is now a
          // different surface as well as a different hue, so "too hot" reads
          // in greyscale.
          const hotX = dbToFraction(HOT_DB) * cssWidth;
          if (w > hotX) hatchHot(ctx, hotX, y, w - hotX, barHeight, p);
        }

        // POSITION, and it is drawn OVER the bar on purpose. The scale ticks
        // above sit under it, which is right for a ruler and useless for a
        // threshold: the moment a signal reaches the amber zone it covers the
        // mark that says where amber starts. These four are the gradient's own
        // break points, so an operator reads level against geometry instead of
        // against hue.
        ctx.fillStyle = p.hold;
        for (const mark of ZONE_BREAKS) {
          const x = Math.round(dbToFraction(mark) * cssWidth);
          // Faint over the empty trough, firm over the lit bar. A mark that is
          // equally loud everywhere is four permanent white lines across a
          // meter that is usually quiet, which is the sort of decoration an eye
          // learns to filter out — and it would be filtering out the thing this
          // is here to say.
          ctx.globalAlpha = x < w ? 0.6 : 0.22;
          ctx.fillRect(x, y, 1, barHeight);
        }
        ctx.globalAlpha = 1;

        // peak-hold tick. Wider once it is at or past clip: at that point the
        // tick is the thing being read, and thickness says so without relying
        // on it having gone red.
        if (st.holdDb > MIN_DB) {
          const clipped = st.holdDb >= CLIP_DB;
          const tickW = clipped ? 3 : 2;
          const px = Math.min(cssWidth - tickW, dbToFraction(st.holdDb) * cssWidth);
          ctx.fillStyle = clipped ? p.peak : p.hold;
          ctx.fillRect(px, y, tickW, barHeight);
        }

        // clip latch: a bright block at full scale that lingers, because a
        // clip you did not see is a clip you cannot fix.
        if (now - st.clipAt < CLIP_LATCH_MS) {
          clipping = true;
          ctx.fillStyle = p.peak;
          ctx.fillRect(cssWidth - 3, y, 3, barHeight);
        }
      }

      // TEXT, the third channel, and the only one that survives the meter being
      // too small to read. Toggled imperatively rather than through state: this
      // runs at animation frame rate across up to 36 channels, and a setState
      // here would re-render the page every time a peak brushed 0 dBFS.
      const flag = clipRef.current;
      if (flag && flag.dataset.on !== String(clipping)) {
        flag.dataset.on = String(clipping);
        flag.style.visibility = clipping ? "visible" : "hidden";
      }

      raf = requestAnimationFrame(draw);
    };

    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, [barHeight, barGap]);

  const channels = rms.length;
  const cssHeight = channels * barHeight + Math.max(0, channels - 1) * barGap;

  return (
    <div className={cn("flex items-start gap-2", className)}>
      {labels && (
        <div
          className="flex shrink-0 flex-col text-[9px] font-mono leading-none text-subtle-foreground"
          style={{ gap: `${barGap}px` }}
        >
          {labels.map((l, i) => (
            <div key={i} className="flex items-center" style={{ height: barHeight }}>
              {l}
            </div>
          ))}
        </div>
      )}
      <div className="relative min-w-0 flex-1">
        <canvas
          ref={canvasRef}
          className="w-full rounded-[2px]"
          style={{ height: cssHeight }}
          aria-label="audio level meter"
        />
        {/* Mounted always and hidden by style, never conditionally rendered:
            the draw loop owns its visibility and has no way to mount a node.
            aria-hidden because the loop toggles it without React knowing, so an
            assertive live region here would be a promise this cannot keep. */}
        <span
          ref={clipRef}
          aria-hidden
          style={{ visibility: "hidden" }}
          className="pointer-events-none absolute right-0 top-0 rounded-bl-[2px] bg-down px-1 font-mono text-[9px] font-semibold leading-[1.4] tracking-wider text-background"
        >
          CLIP
        </span>
      </div>
    </div>
  );
}

/** The dB ruler drawn above a column of meters. Kept separate so a stack of
 *  meters shares one scale instead of repeating it per track.
 *
 *  A zone break gets a brighter number and a tick, because the marks the bar
 *  itself now carries are useless if nothing overhead says what level they
 *  stand at — position is only a channel once it has a legend. */
export function MeterScale({ className }: { className?: string }) {
  const marks = [-60, -50, -40, -30, -20, -12, -6, 0];
  const isBreak = (m: number) => ZONE_BREAKS.includes(m);
  return (
    <div className={cn("relative h-4 w-full select-none", className)}>
      {marks.map((m) => {
        const left = dbToFraction(m) * 100;
        return (
          <div
            key={m}
            className={cn(
              "absolute top-0 font-mono text-[9px] leading-none",
              isBreak(m) ? "text-muted-foreground" : "text-subtle-foreground",
            )}
            style={{
              left: `${left}%`,
              transform:
                m === MIN_DB ? "translateX(0)" : m === MAX_DB ? "translateX(-100%)" : "translateX(-50%)",
            }}
          >
            {m}
          </div>
        );
      })}
      {/* One tick per zone break, INCLUDING the two the number row has no room
          to label. The bar draws a mark at each of these; without the ticks the
          operator sees four lines across every meter and nothing overhead
          saying what level they stand at, which is a pattern rather than a
          legend. */}
      {ZONE_BREAKS.map((m) => (
        <span
          key={`break${m}`}
          aria-hidden
          className="absolute bottom-0 h-1.5 w-px bg-border-strong"
          style={{ left: `${dbToFraction(m) * 100}%` }}
        />
      ))}
    </div>
  );
}
