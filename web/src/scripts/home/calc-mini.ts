/* The homepage calculator: four controls, three outputs, the same formulas as
 * /calculator (src/lib/calc.ts). CalcMini.astro renders the outputs for the
 * default inputs with compute() at build time, so the block is correct with
 * script off; initCalc only recomputes them as the visitor moves a control.
 *
 * There is no invalid state to report. The two bitrates are range sliders. The
 * two counts are steppers around a number box; whatever lands in the box is
 * read through clampInt, so it is always an integer inside its limits, and the
 * box is rewritten to that value when the visitor leaves it.
 *
 * Output text is not a live region while a control moves (that would announce
 * every tick). One polite status line is rewritten when the value settles: on
 * `change`, or on a stepper press. No motion lives here, so the gate is
 * accepted for the shared init shape and not otherwise used. */

import {
  HEADROOM_PCT,
  OVERHEAD_PCT,
  clampInt,
  egressKbps,
  fmtData,
  fmtRate,
  gbFor,
  ingestKbps,
  withMargins,
} from "../../lib/calc";
import type { MotionGate } from "./motion";

export type CalcKey = "video" | "audio" | "tracks" | "dests";
export type CalcInputs = Record<CalcKey, number>;

export const DEFAULTS: CalcInputs = { video: 6000, audio: 160, tracks: 1, dests: 2 };

/** Slider ranges follow the mockup; both sit inside what /calculator accepts (video from 100, audio from 32).
 *  Tracks stop at 6, what OBS sends and the /calculator maximum. */
export const LIMITS: Record<CalcKey, { min: number; max: number; step: number }> = {
  video: { min: 500, max: 20000, step: 100 },
  audio: { min: 32, max: 320, step: 32 },
  tracks: { min: 1, max: 6, step: 1 },
  dests: { min: 1, max: 20, step: 1 },
};

export interface CalcView {
  ingest: string;
  egress: string;
  perHour: string;
  ingestNote: string;
  egressNote: string;
  /** Each bar's length: that leg's figure as a share (0 to 1) of the larger of the two legs. */
  ingestShare: number;
  egressShare: number;
}

export function compute(v: CalcInputs): CalcView {
  const ingestRaw = ingestKbps({ video: v.video, audio: v.audio, tracks: v.tracks });
  const egressRaw = egressKbps({ video: v.video, audio: v.audio, dests: v.dests });
  const margins = `plus ${OVERHEAD_PCT}% overhead and ${HEADROOM_PCT}% headroom`;
  const ingest = withMargins(ingestRaw, OVERHEAD_PCT, HEADROOM_PCT);
  const egress = withMargins(egressRaw, OVERHEAD_PCT, HEADROOM_PCT);
  const top = Math.max(ingest, egress, 1);
  return {
    ingest: fmtRate(ingest),
    egress: fmtRate(egress),
    // Data is what the server sends: overhead counts, headroom does not.
    perHour: fmtData(gbFor(egressRaw, 1, OVERHEAD_PCT)),
    ingestNote: `${fmtRate(ingestRaw)} of media${v.tracks > 1 ? ` across ${v.tracks} audio tracks` : ""}, ${margins}.`,
    egressNote: `${v.dests} destination${v.dests === 1 ? "" : "s"}, ${fmtRate(egressRaw)} of media, ${margins}.`,
    ingestShare: ingest / top,
    egressShare: egress / top,
  };
}

/** Slider fill, 0 to 100, for the --p custom property. */
export function fillPct(key: "video" | "audio", value: number): number {
  const { min, max } = LIMITS[key];
  return ((value - min) / (max - min)) * 100;
}

export function announcement(view: CalcView): string {
  return `Encoder upload ${view.ingest}. Server upload ${view.egress}. Server data ${view.perHour} per hour.`;
}

export function initCalc(root: HTMLElement, _gate: MotionGate): () => void {
  const keys = Object.keys(LIMITS) as CalcKey[];
  const input = (k: CalcKey) => root.querySelector<HTMLInputElement>(`[data-calc-in="${k}"]`);
  const q = (sel: string) => root.querySelector<HTMLElement>(sel);
  if (keys.some((k) => !input(k))) return () => {};

  const read = (k: CalcKey): number => {
    const { min, max } = LIMITS[k];
    return clampInt(input(k)!.value, min, max, DEFAULTS[k]);
  };

  function update() {
    const vals = Object.fromEntries(keys.map((k) => [k, read(k)])) as CalcInputs;
    const view = compute(vals);
    const set = (sel: string, text: string) => {
      const el = q(sel);
      if (el) el.textContent = text;
    };
    set('[data-calc-out="ingest"]', view.ingest);
    set('[data-calc-out="egress"]', view.egress);
    set('[data-calc-out="per-hour"]', view.perHour);
    set('[data-calc-out="ingest-note"]', view.ingestNote);
    set('[data-calc-out="egress-note"]', view.egressNote);
    set('[data-calc-readout="video"]', `${vals.video} kbps`);
    set('[data-calc-readout="audio"]', `${vals.audio} kbps`);
    q('[data-calc-bar="ingest"]')?.style.setProperty("--w", String(view.ingestShare));
    q('[data-calc-bar="egress"]')?.style.setProperty("--w", String(view.egressShare));
    for (const k of ["video", "audio"] as const) {
      input(k)!.style.setProperty("--p", `${fillPct(k, vals[k])}%`);
      input(k)!.setAttribute("aria-valuetext", `${vals[k]} kbps`); // the number alone names no unit
    }
    for (const k of ["tracks", "dests"] as const) {
      const { min, max } = LIMITS[k];
      for (const b of root.querySelectorAll<HTMLButtonElement>(`[data-calc-step="${k}"]`)) {
        /* aria-disabled, never disabled: a button that disables itself while it has
         * focus drops focus to <body>, so a keyboard user holding "+" loses their place. */
        const to = vals[k] + Number(b.dataset.dir);
        b.setAttribute("aria-disabled", String(to < min || to > max));
      }
    }
    return view;
  }

  // The status line is written once the value has settled, not per tick.
  const settle = () => {
    const live = q("[data-calc-live]");
    if (live) live.textContent = announcement(update());
  };

  const onInput = () => void update();
  const onChange = (e: Event) => {
    // Put the box back to the integer that was actually used.
    const el = e.target as HTMLInputElement;
    const key = el.dataset?.calcIn as CalcKey | undefined;
    if (key && key !== "video" && key !== "audio") el.value = String(read(key));
    settle();
  };
  const onClick = (e: Event) => {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-calc-step]");
    if (!b || !root.contains(b) || b.getAttribute("aria-disabled") === "true") return;
    const key = b.dataset.calcStep as CalcKey;
    const { min, max } = LIMITS[key];
    input(key)!.value = String(clampInt(read(key) + Number(b.dataset.dir), min, max, DEFAULTS[key]));
    settle();
  };

  root.addEventListener("input", onInput);
  root.addEventListener("change", onChange);
  root.addEventListener("click", onClick);
  update();
  return () => {
    root.removeEventListener("input", onInput);
    root.removeEventListener("change", onChange);
    root.removeEventListener("click", onClick);
  };
}
