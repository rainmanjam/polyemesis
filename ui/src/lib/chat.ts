import { Gamepad2, MonitorPlay, Radio, ThumbsUp, Tv, Zap } from "lucide-react";
import type { SignalTone } from "@/lib/signal";
import type { ChatMessage, ChatPlatform, ChatStatus } from "@/lib/types";

/* ===========================================================================
   How chat is presented: which accent a platform owns, what a connection state
   reads as, and how a message body splits into text and emotes.

   All of it is a pure function of what the server sent, which is why it is here
   and not in ChatPanel.tsx. The panel renders these answers; it does not decide
   them, and neither does the chat page, which imports two of them directly.

   Splitting them out also lets the panel hot-swap. A file exporting a component
   next to a plain function cannot be, and a full reload drops the WebSocket the
   chat feed is holding open -- so editing a colour used to cost the entire
   scrollback.
   =========================================================================== */

/** Platforms get an accent from the theme's signal palette rather than from
 *  their own brand colours: the kit has five saturated colours and adding four
 *  more hex values for logos would break the one rule the design language has.
 *  The mapping is still mnemonic — YouTube red, Kick green, Facebook blue. */
const ACCENT: Record<
  ChatPlatform,
  {
    label: string;
    icon: React.ComponentType<{ className?: string }>;
    /** Left rule on a message row. */
    rule: string;
    text: string;
    chipOn: string;
  }
> = {
  youtube: {
    label: "YouTube",
    icon: MonitorPlay,
    rule: "border-l-down",
    text: "text-down",
    chipOn: "border-down/40 bg-down-dim text-down",
  },
  twitch: {
    label: "Twitch",
    icon: Gamepad2,
    rule: "border-l-primary",
    text: "text-primary",
    chipOn: "border-primary/50 bg-primary-dim text-foreground",
  },
  kick: {
    label: "Kick",
    icon: Zap,
    rule: "border-l-live",
    text: "text-live",
    chipOn: "border-live/40 bg-live-dim text-live",
  },
  facebook: {
    label: "Facebook",
    icon: ThumbsUp,
    rule: "border-l-armed",
    text: "text-armed",
    chipOn: "border-armed/40 bg-armed-dim text-armed",
  },
  /* TROVO HAS NO CHAT ADAPTER, so nothing is ever rendered with this entry.
     It exists because ChatPlatform is an alias of Platform, and adding Trovo
     as a destination platform widened the Record — TypeScript then requires
     the key whether or not a message can carry it.

     Neutral rather than a sixth accent, per the note above: the kit has five
     saturated tones and they are all spoken for. Whoever wires Trovo's
     websocket chat should choose a real one here. The label is filled in
     regardless, because a row reading "trovo" in lowercase beside five
     capitalised siblings is the fallback firing where a label belongs — the
     exact defect DestinationCard's PLATFORM_LABEL comment records. */
  trovo: {
    label: "Trovo",
    icon: Tv,
    rule: "border-l-border-strong",
    text: "text-muted-foreground",
    chipOn: "border-border-strong bg-secondary text-secondary-foreground",
  },
  custom: {
    label: "Custom",
    icon: Radio,
    rule: "border-l-border-strong",
    text: "text-muted-foreground",
    chipOn: "border-border-strong bg-secondary text-secondary-foreground",
  },
};

/** Unknown platforms fall back rather than crash: the server may know about a
 *  platform this build does not. */
/** Timeout choices. Seconds on the wire, always — the server converts for Kick,
 *  which counts in minutes. The list stops at a day because past that a timeout
 *  is a ban with extra steps, and Kick refuses beyond seven days anyway.
 *
 *  Here rather than beside either consumer: the user card and the right-click
 *  menu both offer these durations, and two copies would drift into a menu
 *  whose "1 hour" is not the card's. */
export const TIMEOUTS: { label: string; seconds: number }[] = [
  { label: "1 min", seconds: 60 },
  { label: "10 min", seconds: 600 },
  { label: "1 hour", seconds: 3600 },
  { label: "1 day", seconds: 86400 },
];

export function accentFor(p: ChatPlatform) {
  return ACCENT[p] ?? ACCENT.custom;
}

export function chatStateTone(state: ChatStatus["state"]): SignalTone {
  switch (state) {
    case "live":
      return "live";
    case "connecting":
    case "degraded":
      return "warn";
    case "failed":
      return "down";
    default:
      return "idle";
  }
}

/** The sentence under a platform's name.
 *
 *  A red dot is not an explanation. Every state that is not plainly live gets
 *  words, and when the adapter did not supply any this says so rather than
 *  inventing a cause it cannot know. */
export function chatStateDetail(s: ChatStatus): string {
  if (s.detail) return s.detail;
  switch (s.state) {
    case "live":
      return "";
    case "connecting":
      return "Connecting.";
    case "degraded":
      return "Running with a limitation the platform did not name.";
    case "failed":
      return s.lastError || "Not connected, and the platform gave no reason.";
    default:
      return "Stopped.";
  }
}

/** Every platform that has either spoken or is attached, in a stable order so
 *  the filter chips do not reshuffle as messages arrive. */
export function platformsIn(messages: ChatMessage[], statuses: ChatStatus[]): ChatPlatform[] {
  const order = Object.keys(ACCENT) as ChatPlatform[];
  const present = new Set<ChatPlatform>();
  statuses.forEach((s) => present.add(s.platform));
  messages.forEach((m) => present.add(m.platform));
  const known = order.filter((p) => present.has(p));
  const unknown = [...present]
    .filter((p) => !order.includes(p))
    .sort((a, b) => a.localeCompare(b));
  return [...known, ...unknown];
}

// -------------------------------------------------------------- text + emotes

export type Piece = { kind: "text"; text: string } | { kind: "emote"; name: string; url?: string };

/** Split a message into text runs and emotes.
 *
 *  Offsets are RUNE offsets with an exclusive end — the convention every
 *  adapter normalises into — so the text is split on code points, not on UTF-16
 *  units. A message with an emoji before an emote misplaces every following
 *  range otherwise. Anything out of range is skipped rather than throwing: a
 *  line rendered without its emote beats a line not rendered at all. */
export function splitMessage(m: ChatMessage): Piece[] {
  const runes = Array.from(m.text);
  const emotes = (m.emotes ?? [])
    .filter((e) => e.start >= 0 && e.end > e.start && e.end <= runes.length)
    .sort((a, b) => a.start - b.start);

  const out: Piece[] = [];
  let at = 0;
  for (const e of emotes) {
    if (e.start < at) continue; // overlapping ranges: keep the first
    if (e.start > at) out.push({ kind: "text", text: runes.slice(at, e.start).join("") });
    out.push({
      kind: "emote",
      name: e.name || runes.slice(e.start, e.end).join(""),
      url: e.url,
    });
    at = e.end;
  }
  if (at < runes.length) out.push({ kind: "text", text: runes.slice(at).join("") });
  return out;
}
