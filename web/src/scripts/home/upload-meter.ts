/* The problem beat's "Upload from home" meters.
 *
 * One number drives four bars: how many platforms the visitor streams to. Only
 * the OBS multi-output plugin uploads a full copy per platform. Cloud relays and
 * nginx/FFmpeg scripts also take one upload from home, so the meters say so;
 * what polyemesis adds is a separate mix per platform on that one upload.
 *
 * Server render is the resting state: 3 platforms, the plugin bar at 3x and
 * over the limit. Motion only runs while the section is on screen and the gate
 * allows it; stopping puts the resting state back. */
import { whileVisible, type MotionGate } from "./motion";

export type MeterKind = "relay" | "plugin" | "scripts" | "polyemesis";

export const START_PLATFORMS = 3;
export const MAX_PLATFORMS = 5;
export const UPLOAD_LIMIT = 2;
export const TICK_MS = 1500;

export function uploadFor(kind: MeterKind, platforms: number): number {
  return kind === "plugin" ? platforms : 1;
}

export function initUploadMeter(root: HTMLElement, gate: MotionGate): () => void {
  const count = root.querySelector("[data-pcount]");
  const bars = [...root.querySelectorAll<HTMLElement>("i[data-kind]")];
  let n = START_PLATFORMS;
  let timer: ReturnType<typeof setInterval> | undefined;

  const paint = () => {
    if (count) count.textContent = String(n);
    for (const bar of bars) {
      const k = uploadFor(bar.dataset.kind as MeterKind, n);
      bar.style.setProperty("--k", String(k / MAX_PLATFORMS));
      bar.classList.toggle("over", k > UPLOAD_LIMIT);
      const val = root.querySelector(`[data-val="${bar.dataset.kind}"]`);
      if (val) val.textContent = `${k}×`;
    }
  };

  const start = () => {
    n = 1;
    paint();
    timer = setInterval(() => {
      n = (n % MAX_PLATFORMS) + 1;
      paint();
    }, TICK_MS);
  };

  const stop = () => {
    clearInterval(timer);
    timer = undefined;
    n = START_PLATFORMS;
    paint();
  };

  return whileVisible(root, gate, start, stop);
}
