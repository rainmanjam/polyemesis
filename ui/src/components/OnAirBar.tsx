import { useState } from "react";
import { Tv } from "lucide-react";

import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/signature/StatusDot";
import { AcrossTheRoom } from "@/components/AcrossTheRoom";
import { attention, onAir } from "@/lib/attention";
import type { SignalTone } from "@/lib/signal";
import type { Status } from "@/lib/types";
import { useStateLabel } from "@/lib/i18n";

/* ===========================================================================
   The first tier of the dashboard: what is on air, then what is wrong.

   Everything below this strip is DETAIL — a preview, an ingest card, a pipeline
   list, a composer, and one card per destination — and all of it was previously
   drawn at the same volume as everything else. An inventory is the right thing
   for a page you are configuring and the wrong thing for a page you are
   watching: the two questions an operator asks under load are "am I on air?"
   and "what is broken?", and both answers were somewhere in the inventory
   rather than at the top of it.

   The strip answers the first in one line and the second in a list that is
   EMPTY when nothing is wrong. That emptiness is the feature. A panel that
   always has something in it is decoration, and an operator stops reading it
   within a week — which costs them the one shift where it mattered.

   Which rows count as wrong is decided in lib/attention.ts, not here.
   =========================================================================== */

/** The headline, and it is a WORD before it is a colour.
 *
 *  "ON AIR" and "OFF AIR" are the two states a broadcast operator already has
 *  language for, so the text carries the reading on its own and the tone dot
 *  beside it is confirmation rather than the message. An operator who cannot
 *  separate the green from the amber still reads the line.
 */
function headline(state: ReturnType<typeof onAir>): { text: string; tone: SignalTone } {
  if (state.live > 0) return { text: "On air", tone: "live" };
  if (state.degraded > 0) return { text: "Coming up", tone: "warn" };
  if (state.failed > 0) return { text: "Off air", tone: "down" };
  // No destination enabled at all is "off air" too, but it is not a fault, and
  // an idle mark rather than a red one is what says so.
  return { text: "Off air", tone: state.enabled > 0 ? "armed" : "idle" };
}

export function OnAirBar({ status }: { status: Status | null | undefined }) {
  const stateLabel = useStateLabel();
  const [roomOpen, setRoomOpen] = useState(false);
  const air = onAir(status?.destinations);
  const items = attention(status);
  const head = headline(air);

  /* Follow a row to the card it describes.
   *
   * Focus rather than a scroll alone: a summary that scrolls the page and then
   * leaves the operator hunting for which of nine cards it meant has moved them
   * without telling them where to. Focus moves the keyboard too, so the same
   * gesture works without a mouse, and the card's focus ring is the "this one"
   * that a scroll cannot say. preventScroll because scrollIntoView has already
   * placed it deliberately in the middle rather than jammed against an edge. */
  const reveal = (id: string) => {
    const card = document.getElementById(id);
    if (!card) return;
    card.scrollIntoView({ block: "center", behavior: "smooth" });
    card.focus({ preventScroll: true });
  };

  return (
    <section
      data-testid="on-air-bar"
      className="mb-3 overflow-hidden rounded-lg border border-border bg-card"
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2">
        <h2 className="flex items-center gap-2">
          <StatusDot tone={head.tone} size="lg" />
          <span className="text-[15px] font-semibold uppercase tracking-wide">{head.text}</span>
        </h2>

        {/* The denominator is enabled destinations, not every row — see onAir().
            Spelled out in words beside the ratio because "2/3" alone is the
            kind of figure that gets read as a bitrate on a console full of
            them. */}
        <span className="tnum font-mono text-[12px] text-muted-foreground">
          <span className="text-foreground">{air.live}</span>/{air.enabled} destinations live
        </span>

        {/* Counts, each with its own mark. Rendered only when non-zero: a
            steady "0 failed" is a number that teaches the eye to skip the
            place where a real one will appear. */}
        {air.degraded > 0 && (
          <span className="flex items-center gap-1.5 text-[12px] text-warn">
            <StatusDot tone="warn" size="sm" />
            {air.degraded} connecting
          </span>
        )}
        {air.failed > 0 && (
          <span className="flex items-center gap-1.5 text-[12px] text-down">
            <StatusDot tone="down" size="sm" />
            {air.failed} failed
          </span>
        )}

        <Button
          variant="outline"
          size="sm"
          className="ml-auto"
          onClick={() => setRoomOpen(true)}
          title="A large-type state view, for a machine sitting beside the desk"
        >
          <Tv /> Across the room
        </Button>
      </div>

      {items.length > 0 && (
        /* Bordered off rather than in the row above: this list is the second
           question, and running it into the first would make a healthy install
           and a broken one the same shape. */
        <ul className="flex flex-col border-t border-border">
          {items.map((item) => {
            const target = item.destinationId ? `dest-${item.destinationId}` : null;
            const body = (
              <>
                <StatusDot tone={item.tone} size="sm" className="mt-1" />
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{item.subject}</span>
                  {item.state && (
                    <span className="ml-1.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                      {stateLabel(item.state)}
                    </span>
                  )}
                  {/* The server's own sentence, never a summary of it. */}
                  {item.detail && (
                    <span className="block truncate text-[11px] text-muted-foreground">
                      {item.detail}
                    </span>
                  )}
                </span>
              </>
            );
            const rowClass = "flex w-full items-start gap-2 px-3 py-1.5 text-left text-[12px]";
            return (
              <li key={item.key} className="border-b border-border last:border-b-0">
                {target ? (
                  <button type="button" onClick={() => reveal(target)} className={`${rowClass} hover:bg-card-raised`}>
                    {body}
                  </button>
                ) : (
                  // No card to go to. A dead button would be worse than none:
                  // the ingest and the renditions are described elsewhere on
                  // the page, not in a card this row could land on.
                  <div className={rowClass}>{body}</div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <AcrossTheRoom open={roomOpen} onClose={() => setRoomOpen(false)} status={status} />
    </section>
  );
}
