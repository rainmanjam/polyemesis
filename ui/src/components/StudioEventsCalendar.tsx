import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { timestamp } from "@/lib/format";
import type { Destination, StudioEventView } from "@/lib/types";

/** The server caches each account's events for 15 minutes, so polling faster than this changes nothing and costs
 *  nothing; this is the most often the page asks. */
export const STUDIO_POLL_MS = 15 * 60_000;

/** YouTube Studio's own upcoming events, in the schedule calendar.
 *
 *  Read-only and visibly apart from polyemesis's own occurrences ("from YouTube Studio"). An event polyemesis
 *  made for one of its own occurrences is left out (its id is in `ownEventIds`), so nothing appears twice. Each
 *  row links to the event in Studio and offers "Use for destination X": the same choice the destination dialog's
 *  "Use an existing YouTube event" makes, stored as that destination's compliance.youtube.eventId. */
export function StudioEventsCalendar({ ownEventIds }: Readonly<{ ownEventIds: ReadonlySet<string> }>) {
  const t = useT();
  const [events, setEvents] = useState<StudioEventView[] | null>(null);
  const [stale, setStale] = useState<string[]>([]);
  const [dests, setDests] = useState<Destination[]>([]);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api.youtubeStudioEvents();
      setEvents(r.events);
      setStale(r.stale);
    } catch {
      // The calendar's own occurrences do not depend on this read, so a failure shows nothing here.
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), STUDIO_POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  useEffect(() => {
    // A failed or malformed read leaves the list as it was (empty): the only effect is that no "Use for" button
    // is offered. The calendar sits on pages whose own reads may answer anything, so a non-list must not throw.
    api.listDestinations().then(
      (rows) => {
        if (Array.isArray(rows)) setDests(rows.map((r) => r.destination));
      },
      () => undefined,
    );
  }, []);

  const shown = (events ?? []).filter((e) => !ownEventIds.has(e.id));
  if (shown.length === 0) return null;

  const use = async (e: StudioEventView, destinationId: number) => {
    const d = dests.find((x) => x.id === destinationId);
    if (!d) return;
    if (d.compliance?.youtube?.studioStreamNow) {
      // polyemesis makes no broadcast for a destination on Studio's Stream now, so a pick would be ignored.
      toast.error(t("studioEvents.studioNow", { destination: d.name }));
      return;
    }
    setBusy(true);
    try {
      // rebindEvent is sent false, not left out: the server merges a save over the stored row, so leaving it out
      // would keep an earlier event's confirmation and move this one off its stream unasked.
      await api.updateDestination(d.id, {
        compliance: { ...d.compliance, youtube: { ...(d.compliance?.youtube ?? {}), eventId: e.id, rebindEvent: false } },
      });
      toast.success(t("studioEvents.used", { title: e.title, destination: d.name }));
    } catch (err) {
      toast.error(t("studioEvents.useFailed", { reason: err instanceof Error ? err.message : "" }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      className="mt-3 rounded border border-dashed border-border p-3"
      aria-label={t("studioEvents.title")}
      data-testid="studio-events"
    >
      <h3 className="mb-2 text-sm font-medium">{t("studioEvents.title")}</h3>
      {stale.length > 0 && (
        <p className="mb-1 text-xs text-muted-foreground">{t("studioEvents.stale", { accounts: stale.join(", ") })}</p>
      )}
      <ul className="grid gap-2">
        {shown.map((e) => {
          const choices = dests.filter(
            (d) => d.platform === "youtube" && d.accountId === e.accountId,
          );
          return (
            <li key={`${e.accountId}-${e.id}`} className="grid gap-1 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="tabular-nums text-muted-foreground">{timestamp(e.scheduledStart)}</span>
                <span className="font-medium">{e.title}</span>
                <span className="rounded bg-muted px-1 text-xs text-muted-foreground">{t("studioEvents.badge")}</span>
                <a className="text-xs underline" href={e.studioUrl} target="_blank" rel="noreferrer">
                  {t("studioEvents.open")}
                </a>
              </div>
              <div className="flex flex-wrap gap-2">
                {choices.map((d) => (
                  <Button key={d.id} size="sm" variant="ghost" disabled={busy} onClick={() => void use(e, d.id)}>
                    {t("studioEvents.useFor", { destination: d.name })}
                  </Button>
                ))}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
