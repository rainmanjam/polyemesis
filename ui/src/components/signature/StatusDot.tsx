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
  return (
    <span className={cn("relative inline-flex shrink-0", dim, className)}>
      {tone === "live" && (
        <span className={cn("absolute inline-flex h-full w-full rounded-full opacity-60 animate-signal", toneBg[tone])} />
      )}
      <span
        className={cn(
          "relative inline-flex h-full w-full rounded-full",
          toneBg[tone],
          tone === "warn" && "animate-signal-fast",
        )}
      />
    </span>
  );
}
