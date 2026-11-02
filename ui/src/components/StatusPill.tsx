import { Circle, CircleDashed, CircleDot, CircleX, type LucideIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";
import type { StatusIndicator, StatusReading, StatusTone } from "@/lib/status";

/* THE ONE STATUS PILL (A11, A91, A73).
 *
 * Renders a StatusReading from lib/status.ts. The word comes from the catalogue
 * and the tone sets the Badge colour; the icon shape is the non-colour cue, so
 * a failed item (cross) is never drawn like a stopped one (ring), even on a
 * greyscale screen. The icon is aria-hidden: the word carries the meaning for
 * screen readers, and the shape is for sighted operators under pressure.
 *
 * Do not hand-roll a status badge on a page. If a state is missing from
 * lib/status.ts, add it there, with a test, so every page reads it the same. */

const TONE_VARIANT: Record<StatusTone, "live" | "warn" | "down" | "outline"> = {
  ok: "live",
  warn: "warn",
  bad: "down",
  idle: "outline",
};

const INDICATOR_ICON: Record<StatusIndicator, LucideIcon> = {
  filled: CircleDot,
  dashed: CircleDashed,
  cross: CircleX,
  ring: Circle,
};

export interface StatusPillProps {
  reading: StatusReading;
  className?: string;
}

/* THREE SHAPES, BY HOW MUCH THE STATE NEEDS THE OPERATOR (2026-11 review).
 *
 *   dot   An ok reading that is the normal case (a process running, a signal
 *         present): neutral text and a 6px --ok dot. A column of identical
 *         filled green pills is status wallpaper that hides the one row that
 *         is wrong, so the nominal state is drawn quietly.
 *   lamp  An ok reading that is ON AIR or RECORDING (reading.lamp): the solid
 *         --tally lamp under its own --tally-foreground label. The tally is a
 *         lamp, never ink (DESIGN-SYSTEM.md).
 *   pill  Everything else keeps its filled or outlined Badge: warn and failed
 *         need a look, and idle keeps its hollow outline.
 *
 * The word and data-tone are the same in all three; only the drawing changes.
 * The silhouette still travels with the reading: the dot is filled (ok's
 * mark), and the lamp and pills keep their indicator icon. */
export function StatusPill({ reading, className }: Readonly<StatusPillProps>) {
  const t = useT();
  const Icon = INDICATOR_ICON[reading.indicator];
  if (reading.tone === "ok" && !reading.lamp) {
    return (
      <span
        data-tone={reading.tone}
        data-shape="dot"
        className={cn("inline-flex items-center gap-1.5 text-sm text-foreground", className)}
      >
        <span data-dot aria-hidden="true" className="h-1.5 w-1.5 shrink-0 rounded-full bg-ok" />
        {t(reading.labelKey)}
      </span>
    );
  }
  if (reading.tone === "ok") {
    return (
      <span
        data-tone={reading.tone}
        data-shape="lamp"
        className={cn(
          "inline-flex items-center gap-1 rounded-sm bg-tally px-1.5 py-0.5 text-sm font-semibold uppercase tracking-caps text-tally-foreground",
          className,
        )}
      >
        <Icon aria-hidden="true" className="h-3 w-3 shrink-0" />
        {t(reading.labelKey)}
      </span>
    );
  }
  return (
    <Badge variant={TONE_VARIANT[reading.tone]} className={className} data-tone={reading.tone} data-shape="pill">
      <Icon aria-hidden="true" className="h-3 w-3 shrink-0" />
      {t(reading.labelKey)}
    </Badge>
  );
}
