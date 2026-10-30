import { useState } from "react";
import { Square, Tv } from "lucide-react";

import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/signature/StatusDot";
import { AcrossTheRoom } from "@/components/AcrossTheRoom";
import { attention, onAir } from "@/lib/attention";
import type { SignalTone } from "@/lib/signal";
import type { FailoverStatus, Status } from "@/lib/types";
import { useStateLabel, useT, type TranslationKey } from "@/lib/i18n";
import { healthVerdict } from "@/lib/healthVerdict";
import { duration } from "@/lib/format";

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
function headline(state: ReturnType<typeof onAir>): { text: TranslationKey; tone: SignalTone } {
  if (state.live > 0) return { text: "onAir.onAir", tone: "live" };
  if (state.degraded > 0) return { text: "onAir.comingUp", tone: "warn" };
  if (state.failed > 0) return { text: "onAir.offAir", tone: "down" };
  // No destination enabled at all is "off air" too, but it is not a fault, and
  // an idle mark rather than a red one is what says so.
  return { text: "onAir.offAir", tone: state.enabled > 0 ? "armed" : "idle" };
}

export function OnAirBar({
  status,
  failover,
  onEndAll,
}: {
  status: Status | null | undefined;
  /** The programme-scoped failover tier, from the Dashboard. Not read from
   *  `status`, which is install-wide and can be another programme's. */
  failover?: FailoverStatus | null;
  /** Opens the dashboard's one Stop all confirmation. The strip only asks; the
   *  count, the YouTube warning and the stop itself stay where they were. */
  onEndAll?: () => void;
}) {
  const stateLabel = useStateLabel();
  const t = useT();
  const [roomOpen, setRoomOpen] = useState(false);
  const air = onAir(status?.destinations);
  const items = attention(status);
  const head = headline(air);
  // The ingest's uptime only means something while it is running. SRT has no
  // ingest process at all (#514), so on a healthy SRT install this is null and
  // the verdict simply omits the stability clause rather than inventing one.
  const ingestUptime = status?.ingest?.state === "running" ? status.ingest.uptimeSec : null;
  const offPrimary = !!failover && failover.active !== "primary" && failover.active !== "";
  const verdict = healthVerdict(air, items, ingestUptime, offPrimary);
  // How long the broadcast has been on air: the longest-running destination,
  // since the first one up is when the audience could first see it.
  const onAirSec = (status?.destinations ?? []).reduce(
    (max, d) => (d.process?.state === "running" ? Math.max(max, d.process.uptimeSec ?? 0) : max),
    0,
  );

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
          <span className="text-[15px] font-semibold uppercase tracking-wide">{t(head.text)}</span>
          {air.live > 0 && onAirSec > 0 && (
            <span className="tnum font-mono text-[13px] font-normal text-muted-foreground" data-testid="on-air-duration">
              {duration(onAirSec)}
            </span>
          )}
        </h2>

        {/* The denominator is enabled destinations, not every row — see onAir().
            Spelled out in words beside the ratio because "2/3" alone is the
            kind of figure that gets read as a bitrate on a console full of
            them. */}
        <span className="tnum font-mono text-[12px] text-muted-foreground">
          <span className="text-foreground">{air.live}</span>/{air.enabled} {t("i18nSweep.destinationsLive")}
        </span>

        {/* Counts, each with its own mark. Rendered only when non-zero: a
            steady "0 failed" is a number that teaches the eye to skip the
            place where a real one will appear. */}
        {air.degraded > 0 && (
          <span className="flex items-center gap-1.5 text-[12px] text-warn">
            <StatusDot tone="warn" size="sm" />
            {t("i18nSweep.connectingCount", { count: air.degraded })}
          </span>
        )}
        {air.failed > 0 && (
          <span className="flex items-center gap-1.5 text-[12px] text-down">
            <StatusDot tone="down" size="sm" />
            {t("i18nSweep.failedCount", { count: air.failed })}
          </span>
        )}

        {/* THE ANSWER TO "IS EVERYTHING FINE, AND WHAT NEEDS ME?" in one line.
            Healthy says so and how long the ingest has held; attention says how
            many and gives a way to the first one. Derived in lib/healthVerdict,
            from the same rows the list below is drawn from. */}
        {verdict?.kind === "healthy" && (
          <span className="flex items-center gap-1.5 text-[12px] text-foreground">
            <StatusDot tone="live" size="sm" />
            <span data-testid="health-verdict">
              {t("health.verdictHealthy", { count: verdict.count })}
            </span>
            {verdict.uptimeSec !== null && (
              <span className="tnum font-mono text-muted-foreground">
                {t("health.verdictIngest", { uptime: duration(verdict.uptimeSec) })}
              </span>
            )}
          </span>
        )}
        {verdict?.kind === "attention" && (
          <span className="flex items-center gap-2 text-[12px] text-warn">
            <StatusDot tone="warn" size="sm" />
            <span data-testid="health-verdict">
              {t("health.verdictAttention", { count: verdict.count, total: verdict.total })}
            </span>
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-[12px]"
              onClick={() => reveal(`dest-${verdict.firstDestinationId}`)}
            >
              {t("health.verdictShow")}
            </Button>
          </span>
        )}

        {/* End all, beside the reading it ends (A8). Only while something is on
            air: an End button over an idle install is a control with nothing
            to do. It opens the same confirmation as Stop all. */}
        {air.live > 0 && onEndAll && (
          <Button variant="outline" size="sm" className="ml-auto text-down" onClick={onEndAll}>
            <Square /> {t("onAir.endAll")}
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          className={air.live > 0 && onEndAll ? undefined : "ml-auto"}
          onClick={() => setRoomOpen(true)}
          title={t("i18nSweep.acrossTheRoomHint")}
        >
          <Tv /> {t("i18nSweep.acrossTheRoom")}
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
