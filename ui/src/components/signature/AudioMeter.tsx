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

/** Colour breaks at levels a mixer cares about, not at even fractions. */
function buildGradient(ctx: CanvasRenderingContext2D, w: number, p: Palette) {
  const g = ctx.createLinearGradient(0, 0, w, 0);
  g.addColorStop(0, p.low);
  g.addColorStop(dbToFraction(-18), p.low);
  g.addColorStop(dbToFraction(-12), p.mid);
  g.addColorStop(dbToFraction(-6), p.high);
  g.addColorStop(dbToFraction(-2), p.peak);
  g.addColorStop(1, p.peak);
  return g;
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
        stateRef.current.push({ holdDb: MIN_DB, holdSetAt: 0, clipAt: 0 });
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
        }

        // peak-hold tick
        if (st.holdDb > MIN_DB) {
          const px = Math.min(cssWidth - 2, dbToFraction(st.holdDb) * cssWidth);
          ctx.fillStyle = st.holdDb >= CLIP_DB ? p.peak : p.hold;
          ctx.fillRect(px, y, 2, barHeight);
        }

        // clip latch: a bright block at full scale that lingers, because a
        // clip you did not see is a clip you cannot fix.
        if (now - st.clipAt < CLIP_LATCH_MS) {
          ctx.fillStyle = p.peak;
          ctx.fillRect(cssWidth - 3, y, 3, barHeight);
        }
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
      <canvas
        ref={canvasRef}
        className="w-full rounded-[2px]"
        style={{ height: cssHeight }}
        aria-label="audio level meter"
      />
    </div>
  );
}

/** The dB ruler drawn above a column of meters. Kept separate so a stack of
 *  meters shares one scale instead of repeating it per track. */
export function MeterScale({ className }: { className?: string }) {
  const marks = [-60, -50, -40, -30, -20, -12, -6, 0];
  return (
    <div className={cn("relative h-3 w-full select-none", className)}>
      {marks.map((m) => {
        const left = dbToFraction(m) * 100;
        return (
          <div
            key={m}
            className="absolute top-0 text-[9px] font-mono leading-none text-subtle-foreground"
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
    </div>
  );
}
