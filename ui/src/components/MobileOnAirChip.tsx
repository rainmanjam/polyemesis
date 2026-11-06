import { useEffect, useRef, useState, type RefObject } from "react";
import { StatusDot } from "@/components/signature/StatusDot";
import { useT } from "@/lib/i18n";
import type { HeaderLive } from "@/lib/headerLive";
import { cn } from "@/lib/utils";
import { chipFits } from "@/features/shell/chipFit";

/** Re-measures with a ResizeObserver on the row, the yardstick and every item
 *  beside the chip (a switcher whose label settles after the programme list
 *  arrives changes the room without changing the row's size). The observer
 *  fires once on observe, so nothing is set synchronously in the effect.
 *  Without a ResizeObserver (jsdom) it reports "fits": the full words. */
function useChipFit(
  chipRef: RefObject<HTMLSpanElement | null>,
  measureRef: RefObject<HTMLSpanElement | null>,
  active: boolean,
  words: string,
): boolean {
  const [fits, setFits] = useState(true);
  useEffect(() => {
    const chip = chipRef.current;
    const measure = measureRef.current;
    const row = chip?.parentElement;
    if (!active || !chip || !measure || !row || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setFits(chipFits(chip, measure)));
    ro.observe(row);
    ro.observe(measure);
    for (const c of Array.from(row.children)) if (c !== chip) ro.observe(c);
    return () => ro.disconnect();
  }, [chipRef, measureRef, active, words]);
  return fits;
}

/* THE PHONE'S ANSWER TO "AM I ON AIR?" (A78), beside the hamburger.
 *
 * One line, always: with the programme in it ("Main: Off air") the chip once
 * wrapped onto three lines at 390px.
 *
 * NAMED ONCE. When the count is scoped to a programme, the programme switcher
 * beside this already shows its name, so the chip printed it twice ("● Main
 * Pr…" and "Main Programme"). It keeps the dot and the state words; the
 * scoped sentence, programme included, is sr-only and in the title.
 *
 * NEVER CUT TO AN ELLIPSIS. "O…" said nothing. The full words ("Off air",
 * "3 live") are measured against the room the header has and, where they do
 * not fit, the short form ("Off", "3 live") is printed instead, whole. In that
 * case the chip does not shrink; the switcher beside it gives way, and its
 * menu names every programme in full. */
export function MobileOnAirChip({ live }: Readonly<{ live: HeaderLive }>) {
  const t = useT();
  const on = live.count > 0;
  // Before the first status frame there is nothing to count, and none is not
  // "Off air": the words say the reading is on its way instead.
  const loading = t("common.loading");
  let sentence: string;
  if (!live.known) sentence = loading;
  else if (on) sentence = t(live.key, live.params);
  else sentence = t(live.offKey, live.offParams);
  // Scoped to one named programme: exactly when the programme switcher shows.
  const scoped = live.key === "chrome.liveCountProgramme";
  let full: string;
  let short: string;
  if (!live.known) {
    full = loading;
    short = loading;
  } else if (on) {
    full = t("chrome.liveCount", { count: live.count });
    short = t("chrome.liveCountShort", { count: live.count });
  } else {
    full = t("onAir.offAir");
    short = t("chrome.offAirShort");
  }
  const chipRef = useRef<HTMLSpanElement>(null);
  const measureRef = useRef<HTMLSpanElement>(null);
  const fits = useChipFit(chipRef, measureRef, scoped, full);
  const tone = on ? "text-foreground" : "text-muted-foreground";

  return (
    <span
      ref={chipRef}
      data-testid="mobile-on-air"
      className={cn(
        "relative flex min-w-1.5 items-center gap-1.5 whitespace-nowrap text-tiny sm:hidden",
        scoped && "shrink-0",
      )}
      title={sentence}
    >
      {/* shrink-0: the dot is the part that must survive any squeeze. */}
      {/* On air is the tally lamp, as on the desktop live count, the ON AIR
          bar and a destination card (row 10): a filled red disc. Green would
          say "healthy", which is a different fact. Off air keeps the hollow
          idle ring, so the two differ in shape as well as hue, and the words
          beside them differ again. */}
      <span className="flex shrink-0">
        {on ? (
          <span
            data-testid="mobile-on-air-dot"
            data-tone="tally"
            aria-hidden="true"
            className="inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-tally"
          />
        ) : (
          <span data-testid="mobile-on-air-dot" data-tone="idle" className="inline-flex">
            <StatusDot tone="idle" size="sm" />
          </span>
        )}
      </span>
      {scoped ? (
        <>
          <span className="sr-only">{sentence}</span>
          <span
            aria-hidden="true"
            data-testid="mobile-on-air-words"
            data-fit={fits ? "full" : "short"}
            className={cn("whitespace-nowrap", tone)}
          >
            {fits ? full : short}
          </span>
          {/* The yardstick: the full words, laid out but never seen. Fixed,
              so it adds nothing to the chip's own scroll width. */}
          <span
            ref={measureRef}
            aria-hidden="true"
            className="pointer-events-none invisible fixed left-0 top-0 whitespace-nowrap"
          >
            {full}
          </span>
        </>
      ) : (
        <span className={cn("truncate", tone)}>{sentence}</span>
      )}
    </span>
  );
}
