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
