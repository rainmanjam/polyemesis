import { Circle, CircleDashed, CircleDot, CircleX, type LucideIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
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

export function StatusPill({ reading, className }: Readonly<StatusPillProps>) {
  const t = useT();
  const Icon = INDICATOR_ICON[reading.indicator];
  return (
    <Badge variant={TONE_VARIANT[reading.tone]} className={className} data-tone={reading.tone}>
      <Icon aria-hidden="true" className="h-3 w-3 shrink-0" />
      {t(reading.labelKey)}
    </Badge>
  );
}
