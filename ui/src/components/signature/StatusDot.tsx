import { cn } from "@/lib/utils";
import { toneBg, type SignalTone } from "@/lib/signal";

/** The single "is it on air" indicator, used everywhere a state is shown.
 *  Live breathes slowly; reconnecting blinks urgently; everything else is
 *  static. Motion is a signal here, not decoration. */
export function StatusDot({
  tone,
  size = "md",
  className,
}: {
  tone: SignalTone;
  size?: "sm" | "md" | "lg";
  className?: string;
}) {
  const dim = size === "sm" ? "h-1.5 w-1.5" : size === "lg" ? "h-3 w-3" : "h-2 w-2";
  // THE HALO PULSES, NEVER THE DOT.
  //
  // `live` was already built this way — a separate absolutely-positioned halo
  // animates while the core stays fully opaque. `warn` was not: it put
  // `animate-signal-fast` on the CORE, and signal-pulse takes opacity to 0.35
  // at its midpoint. So a RECONNECTING destination — the state that most needs
  // attention — had its only indicator half-disappear twice a second.
  //
  // That is the shape of the `text-ok` bug this project has already shipped
  // once: the more urgent state was the one rendered harder to see. The two
  // tones now share one structure, so the asymmetry cannot come back by
  // someone editing a single branch.
  const pulse = tone === "live" ? "animate-signal" : tone === "warn" ? "animate-signal-fast" : null;
  return (
    <span className={cn("relative inline-flex shrink-0", dim, className)}>
      {pulse && (
        <span
          className={cn(
            "absolute inline-flex h-full w-full rounded-full opacity-60",
            pulse,
            toneBg[tone],
          )}
        />
      )}
      <span className={cn("relative inline-flex h-full w-full rounded-full", toneBg[tone])} />
    </span>
  );
}
