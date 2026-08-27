import { cn } from "@/lib/utils";
import { toneMark, type SignalTone } from "@/lib/signal";

/** The single "is it on air" indicator, used everywhere a state is shown.
 *  Live breathes slowly; reconnecting blinks urgently; everything else is
 *  static. Motion is a signal here, not decoration.
 *
 *  SHAPE IS THE SECOND CHANNEL, and it is not decoration either. Until this
 *  carried one, hue was the only thing separating a live destination from a
 *  failed one — the same class of bug as the `text-ok` badge this repo already
 *  shipped once, except that no test can catch it because both dots render
 *  perfectly. See toneMark in lib/signal.ts for the five silhouettes. */
export function StatusDot({
  tone,
  size = "md",
  className,
  title,
}: {
  tone: SignalTone;
  size?: "sm" | "md" | "lg" | "xl";
  className?: string;
  /** Overrides the tone's own wording, for a dot whose row can say something
   *  more specific than "Failed" -- a rendition nobody has enabled, say. */
  title?: string;
}) {
  const dim =
    size === "sm"
      ? "h-1.5 w-1.5"
      : size === "lg"
        ? "h-3 w-3"
        : size === "xl"
          ? "h-8 w-8"
          : "h-2 w-2";
  const mark = toneMark[tone];
  // A ring scaled with the dot. At 6px a 2px border leaves a 2px hole that
  // closes up into a solid dot at any distance, which loses exactly the
  // distinction the hollow tones exist to make.
  const ring = mark.hollow
    ? size === "sm"
      ? "border-[1.5px]"
      : size === "xl"
        ? "border-4"
        : "border-2"
    : null;
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
  // A THIRD CHANNEL, in words. Hue and silhouette both require knowing the
  // vocabulary before they say anything, and neither reaches a screen reader.
  // The wording lives on toneMark in lib/signal.ts so a new tone cannot be
  // added without one.
  return (
    <span
      className={cn("relative inline-flex shrink-0", dim, className)}
      title={title ?? mark.title}
      role="img"
      aria-label={title ?? mark.title}
    >
      {pulse && (
        <span className={cn("absolute inline-flex h-full w-full opacity-60", pulse, mark.shape)} />
      )}
      <span className={cn("relative inline-flex h-full w-full", mark.shape, ring)} />
    </span>
  );
}
