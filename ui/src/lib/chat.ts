import { Radio, Tv } from "lucide-react";
import {
  FacebookGlyph,
  KickGlyph,
  TwitchGlyph,
  XGlyph,
  YouTubeGlyph,
} from "@/components/platformGlyphs";
import type { TranslationKey } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";
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
 *  The mapping is still mnemonic — YouTube red, Kick green, Facebook blue.
 *
 *  THE ICONS ARE THE PLATFORMS' OWN, and that is not a contradiction. The rule
 *  above is about adding saturated COLOURS to a palette that has five; it never
 *  required the marks to be invented. They were lucide approximations, and
 *  three of the four read as something else entirely -- a thumbs-up is a
 *  reaction, a gamepad is a games category, a bolt is a status. Drawn in
 *  currentColor, so the shape says which platform and the accent stays the
 *  kit's. See components/platformGlyphs.tsx. */
const ACCENT: Record<
  ChatPlatform,
  {
    icon: React.ComponentType<{ className?: string }>;
    /** Left rule on a message row. */
    rule: string;
    text: string;
    chipOn: string;
  }
> = {
  youtube: {
    icon: YouTubeGlyph,
    rule: "border-l-down",
    text: "text-down",
    chipOn: "border-down/40 bg-down-dim text-down",
  },
  twitch: {
    icon: TwitchGlyph,
    rule: "border-l-primary",
    text: "text-primary-bright",
    chipOn: "border-primary/50 bg-primary-dim text-foreground",
  },
  kick: {
    icon: KickGlyph,
    rule: "border-l-ok",
    text: "text-ok",
    chipOn: "border-ok/40 bg-ok-dim text-ok",
  },
  facebook: {
    icon: FacebookGlyph,
    // An identity colour, not a state: --cat-cyan, never --idle (grey).
    rule: "border-l-cat-cyan",
    text: "text-cat-cyan",
    chipOn: "border-cat-cyan/40 bg-cat-cyan-dim text-cat-cyan",
  },
  /* X (Twitter) Live chat, delivered to polyemesis's webhook once X Livestream
     access is on. Neutral, like every platform beyond the four with an accent
     of their own: the palette has no colour to spare. */
  x: {
    icon: XGlyph,
    rule: "border-l-border-strong",
    text: "text-foreground",
    chipOn: "border-border-strong bg-secondary text-secondary-foreground",
  },
  /* Rumble chat is read and moderated locally; it has no accent of its own and
     no brand glyph here. Until now it fell through to the unknown-platform
     accent and was labelled "Custom". */
  rumble: {
    icon: Tv,
    rule: "border-l-border-strong",
    text: "text-foreground",
    chipOn: "border-border-strong bg-secondary text-secondary-foreground",
  },
};

/** A platform this build has no accent for: the server may know about one it
 *  does not. Shown plainly and named by what the server called it. */
const UNKNOWN_ACCENT = {
  icon: Radio,
  rule: "border-l-border-strong",
  text: "text-muted-foreground",
  chipOn: "border-border-strong bg-secondary text-secondary-foreground",
};

/** Timeout choices. Seconds on the wire, always — the server converts for Kick,
 *  which counts in minutes. The list stops at a day because past that a timeout
 *  is a ban with extra steps, and Kick refuses beyond seven days anyway.
 *
 *  Here rather than beside either consumer: the user card and the right-click
 *  menu both offer these durations, and two copies would drift into a menu
 *  whose "1 hour" is not the card's. */
export const TIMEOUTS: { label: TranslationKey; seconds: number }[] = [
  { label: "chat.timeout1Min", seconds: 60 },
  { label: "chat.timeout10Min", seconds: 600 },
  { label: "chat.timeout1Hour", seconds: 3600 },
  { label: "chat.timeout1Day", seconds: 86400 },
];

/** How a platform is drawn in chat. The name comes from the registry (lib/platforms),
 *  so there is one list of names. */
export function accentFor(p: ChatPlatform) {
  return { ...(Object.hasOwn(ACCENT, p) ? ACCENT[p] : UNKNOWN_ACCENT), label: platformName(p) };
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
