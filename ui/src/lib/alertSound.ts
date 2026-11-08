/** The alerts overlay's sounds, synthesised with the Web Audio API: no audio
 *  files and no URL, so there is nothing to fetch, nothing to block and nothing
 *  an overlay key could be made to load. Each is a few short oscillator notes.
 *
 *  NEVER THROWS. An OBS browser source may have no audio context at all, or one
 *  the browser keeps suspended until a gesture; a sound that cannot play is a
 *  silent alert, and an alert that cannot draw because its sound threw would be
 *  a far worse failure. */

import type { AlertSound } from "./types";

interface Note {
  /** Hz. */
  f: number;
  /** Seconds after the start. */
  at: number;
  /** Seconds the note sounds. */
  len: number;
  wave: OscillatorType;
  gain: number;
}

/** The patterns, exported so a test can see they are all short and bounded. */
export const SOUND_PATTERNS: Record<Exclude<AlertSound, "none">, Note[]> = {
  chime: [
    { f: 880, at: 0, len: 0.25, wave: "sine", gain: 0.18 },
    { f: 1320, at: 0.12, len: 0.4, wave: "sine", gain: 0.16 },
  ],
  pop: [{ f: 520, at: 0, len: 0.09, wave: "triangle", gain: 0.22 }],
  bell: [
    { f: 660, at: 0, len: 0.9, wave: "sine", gain: 0.2 },
    { f: 1320, at: 0, len: 0.5, wave: "sine", gain: 0.08 },
  ],
};

type Ctor = typeof AudioContext;

let shared: AudioContext | null = null;

function context(): AudioContext | null {
  if (shared) return shared;
  const w = globalThis as unknown as { AudioContext?: Ctor; webkitAudioContext?: Ctor };
  const C = w.AudioContext ?? w.webkitAudioContext;
  if (!C) return null;
  try {
    shared = new C();
  } catch {
    return null;
  }
  return shared;
}

/** Plays a sound. Resolves true if notes were scheduled, false for "none", no
 *  audio support, a context that would not start, or any error. */
export async function playAlertSound(sound: AlertSound): Promise<boolean> {
  if (sound === "none") return false;
  const pattern = SOUND_PATTERNS[sound];
  if (!pattern) return false;
  try {
    const ctx = context();
    if (!ctx) return false;
    // Suspended until a user gesture on most browsers; OBS allows autoplay. A
    // refusal leaves the alert silent, which is the correct degradation.
    if (ctx.state === "suspended") await ctx.resume();
    // A context the browser still will not start schedules notes that never
    // sound; only a closed one is worth refusing outright.
    if (ctx.state === "closed") return false;
    const t0 = ctx.currentTime;
    for (const n of pattern) {
      const osc = ctx.createOscillator();
      const g = ctx.createGain();
      osc.type = n.wave;
      osc.frequency.value = n.f;
      // A short attack and a decay to silence, or each note ends in a click.
      g.gain.setValueAtTime(0.0001, t0 + n.at);
      g.gain.linearRampToValueAtTime(n.gain, t0 + n.at + 0.01);
      g.gain.exponentialRampToValueAtTime(0.0001, t0 + n.at + n.len);
      osc.connect(g);
      g.connect(ctx.destination);
      osc.start(t0 + n.at);
      osc.stop(t0 + n.at + n.len + 0.02);
    }
    return true;
  } catch {
    return false;
  }
}

/** Test seam: forget the shared context. */
export function resetAlertAudio(): void {
  shared = null;
}
