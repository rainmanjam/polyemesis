import type { ProcessState } from "./types";

/** The single mapping from process state to the theme's signal language.
 *  Every status pill, dot and border colour in the app resolves through here,
 *  so "green means live" is enforced in one place rather than per component. */
export type SignalTone = "live" | "warn" | "down" | "armed" | "idle";

export function toneForState(state?: ProcessState | null): SignalTone {
  switch (state) {
    case "running":
      return "live";
    case "reconnecting":
    case "starting":
      return "warn";
    case "failed":
      return "down";
    case "stopped":
      return "idle";
    default:
      return "idle";
  }
}

export function labelForState(state?: ProcessState | null): string {
  switch (state) {
    case "running":
      return "Live";
    case "reconnecting":
      return "Reconnecting";
    case "starting":
      return "Starting";
    case "failed":
      return "Failed";
    case "stopped":
      return "Stopped";
    default:
      return "Offline";
  }
}

export const toneText: Record<SignalTone, string> = {
  live: "text-live",
  warn: "text-warn",
  down: "text-down",
  armed: "text-armed",
  idle: "text-subtle-foreground",
};

export const toneBg: Record<SignalTone, string> = {
  live: "bg-live",
  warn: "bg-warn",
  down: "bg-down",
  armed: "bg-armed",
  idle: "bg-subtle-foreground",
};

export const toneBadge: Record<SignalTone, "live" | "warn" | "down" | "armed" | "outline"> = {
  live: "live",
  warn: "warn",
  down: "down",
  armed: "armed",
  idle: "outline",
};

/** The SILHOUETTE for a tone, which is the second channel WCAG 1.4.1 asks for.
 *
 *  Everything above encodes state as hue and nothing else, so the five states
 *  are one deuteranopic operator — or one laptop screen in daylight — away from
 *  being one state. Five silhouettes that survive being printed in greyscale:
 *
 *      live ●   warn ◆   down ■   armed ○   idle □
 *
 *  Shape rather than a glyph font because these render at 6-12px inside a
 *  status dot, where a character would be a smudge, and because the same record
 *  then scales to the across-the-room view at 40px without a second vocabulary.
 *
 *  `hollow` is a flag rather than a border width in the class string: the ring
 *  has to be thinner at 6px than at 40px or it closes up into a solid dot, and
 *  that is a decision for the component that knows the size. */
export const toneMark: Record<SignalTone, { shape: string; hollow: boolean }> = {
  // Solid disc. The only tone that fills its box completely, which is what
  // makes "on air" the heaviest mark on the screen at a glance.
  live: { shape: "rounded-full bg-live", hollow: false },
  // Diamond. scale-90 trims it back toward the optical weight of the disc
  // beside it: a square turned 45° has a 1.41x diagonal, so at full size a
  // reconnecting dot reads as a bigger mark than a live one — which is a
  // hierarchy nobody meant to state. Transform only, so nothing here can move
  // the label beside it whatever the scale.
  warn: { shape: "rotate-45 scale-90 rounded-[1px] bg-warn", hollow: false },
  down: { shape: "rounded-[1px] bg-down", hollow: false },
  // Hollow: nothing is flowing through it yet. Reads as "loaded, not firing"
  // without needing its hue.
  armed: { shape: "rounded-full border-armed", hollow: true },
  idle: { shape: "rounded-[1px] border-subtle-foreground", hollow: true },
};
