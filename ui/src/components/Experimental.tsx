import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";

/* ==========================================================================
   The EXPERIMENTAL convention
   ==========================================================================

   Two things shipped in 0.7.0 that nobody has watched work on real hardware:
   Twitch Enhanced Broadcasting (measured only against httptest fixtures, never
   against Twitch's live endpoint) and the hardware encoder flags (read out of
   FFmpeg's own option tables inside a container, never confirmed by an NVENC,
   QSV or VA-API encode). Both are reachable, both are supposed to stay
   reachable, and neither has evidence behind it yet.

   SO THIS LABELS, IT DOES NOT GATE. There is no flag that turns either feature
   off, nothing is hidden, and no opt-in is required. An operator who wants to
   try them still can, in one click, exactly as before. The only thing added is
   that the control now says what has not been checked.

   ONE BADGE, ONE SENTENCE, AND THE SENTENCE NAMES THE UNTESTED CLAIM. "Beta"
   tells a reader to be vaguely nervous and gives them nothing to act on. "The
   negotiation has never run against Twitch's live endpoint" tells them exactly
   which part is a guess and therefore exactly what a failure would mean. Every
   use of <Experimental> in this codebase carries that specific sentence; a use
   that only says "experimental" is a bug in the copy.

   NOT A SIGNAL COLOUR. The app's five saturated tokens -- live, warn, down,
   armed, idle -- mean the state of a destination. "Unverified on hardware" is
   a property of the FEATURE and not of any running thing, so painting it amber
   would put it in a vocabulary that already means something else and would
   train an operator to read a healthy broadcast as broken. `outline` is the
   neutral variant, and the words carry the weight.

   Mirrored outside the UI by:
     - Go:       an `// EXPERIMENTAL: <what is unverified>` line in the doc
                 comment of the package or declaration.
     - docs/:    a `> **EXPERIMENTAL — <claim>.**` blockquote under the heading.
     - CHANGELOG a leading `**EXPERIMENTAL.**` sentence on the entry.
   ========================================================================== */

/** The badge on its own, for a heading or a row that already carries the
 *  explanation somewhere the reader can see it. */
export function ExperimentalBadge({ className }: { className?: string }) {
  return (
    <Badge variant="outline" className={className}>
      experimental
    </Badge>
  );
}

/** The badge plus the specific claim that has not been verified.
 *
 *  `children` is that claim, and it is required rather than optional: the whole
 *  point of the convention is that the label is useless without it. */
export function Experimental({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border bg-card-raised p-2">
      <ExperimentalBadge className="self-start" />
      <span className="text-[10px] text-muted-foreground">{children}</span>
    </div>
  );
}
