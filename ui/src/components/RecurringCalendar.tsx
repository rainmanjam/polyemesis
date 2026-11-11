/* ===========================================================================
   The calendar of upcoming recurring events (#18).

   It lists the next two weeks of occurrences from the server's calendar read,
   and lets the operator skip one or retitle one. Skip and retitle are the two
   writes the server accepts for a single occurrence, and each one's refusal is
   shown as what it is:
   - 409: the server refused for a reason the operator can act on (the show has
     started, is live, or its event is still being created). Shown as "not removed"
     with the server's sentence.
   - 502: the platform answered and refused. Shown as the platform's refusal.
   - anything else: a plain failure.

   A pending occurrence has no event id yet, so it can be neither skipped nor
   retitled from here, and its row says so.
   =========================================================================== */

import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT, type TranslationKey } from "@/lib/i18n";
import { classifySkip } from "@/lib/recurringOutcome";
import { listOccurrences, retitleOccurrence, skipOccurrence } from "@/lib/recurringApi";
import type { OccurrenceView } from "@/lib/types";
import { timestamp } from "@/lib/format";
import { StudioEventsCalendar } from "@/components/StudioEventsCalendar";

/** Two weeks, the same default window the server uses when no bounds are sent. */
const WINDOW_MS = 14 * 24 * 3_600_000;

const STATE_KEY: Record<OccurrenceView["state"], TranslationKey> = {
  upcoming: "wave3.recurring.stateUpcoming",
  pending: "wave3.recurring.statePending",
  skipped: "wave3.recurring.stateSkipped",
};

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function RecurringCalendar() {
  const t = useT();
  const [rows, setRows] = useState<OccurrenceView[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [skipTarget, setSkipTarget] = useState<OccurrenceView | null>(null);
  const [editing, setEditing] = useState<{ id: number; title: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const now = new Date();
    try {
      const out = await listOccurrences(now, new Date(now.getTime() + WINDOW_MS));
      setRows(out);
      setLoadError("");
    } catch (err) {
      setLoadError(messageOf(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Reports a write's refusal as its own sentence, so a 409 never reads like a platform failure.
  const reportRefusal = (err: unknown, retitle: boolean) => {
    const c = classifySkip(err);
    if (c.kind === "refused") {
      toast.error(
        retitle
          ? t("wave3.recurring.retitleRefused", { reason: messageOf(err) })
          : t("wave3.recurring.skipRefused", { reason: messageOf(err) }),
      );
    } else if (c.kind === "platform") {
      toast.error(retitle ? t("wave3.recurring.retitlePlatform") : t("wave3.recurring.skipPlatform"));
    } else {
      toast.error(retitle ? t("wave3.recurring.retitleFailed", { reason: messageOf(err) }) : t("wave3.recurring.skipFailed"));
    }
  };

  const doSkip = async (o: OccurrenceView) => {
    setBusy(true);
    try {
      await skipOccurrence(o.id);
      toast.success(t("wave3.recurring.skipped"));
      await load();
    } catch (err) {
      reportRefusal(err, false);
    } finally {
      setBusy(false);
    }
  };

  const doRetitle = async (o: OccurrenceView, title: string) => {
    setBusy(true);
    try {
      await retitleOccurrence(o.id, title);
      toast.success(t("wave3.recurring.retitleDone"));
      setEditing(null);
      await load();
    } catch (err) {
      reportRefusal(err, true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="rounded border border-border p-3" aria-label={t("wave3.recurring.calendarTitle")}>
      <h3 className="mb-2 text-sm font-medium">{t("wave3.recurring.calendarTitle")}</h3>
      {loadError ? <p role="alert" className="text-sm text-down">{loadError}</p> : null}
      {rows?.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("wave3.recurring.calendarEmpty")}</p>
      ) : null}
      <ul className="grid gap-2">
        {(rows ?? []).map((o) => {
          const hasEvent = o.state === "upcoming" && o.eventId !== "";
          const isEditing = editing?.id === o.id;
          return (
            <li key={o.id} className="grid gap-1 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="tabular-nums text-muted-foreground">{timestamp(o.at)}</span>
                <span className="font-medium">{o.title || t("wave3.recurring.noEventYet")}</span>
                <span className="text-xs text-muted-foreground">{t(STATE_KEY[o.state])}</span>
              </div>
              {isEditing ? (
                <div className="flex flex-wrap items-center gap-2">
                  <Input
                    aria-label={t("wave3.recurring.retitle")}
                    value={editing.title}
                    maxLength={100}
                    onChange={(e) => setEditing({ id: o.id, title: e.target.value })}
                  />
                  <Button size="sm" disabled={busy || editing.title.trim() === ""} onClick={() => void doRetitle(o, editing.title)}>
                    {t("wave3.recurring.retitleSave")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>
                    {t("common.cancel")}
                  </Button>
                </div>
              ) : null}
              {hasEvent ? (
                <div className="flex gap-2">
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing({ id: o.id, title: o.title })}>
                    {t("wave3.recurring.retitle")}
                  </Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setSkipTarget(o)}>
                    {t("wave3.recurring.skip")}
                  </Button>
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
      <StudioEventsCalendar ownEventIds={new Set((rows ?? []).map((o) => o.eventId).filter((id) => id !== ""))} />
      <ConfirmDestructive
        open={skipTarget !== null}
        onOpenChange={(open) => {
          if (!open) setSkipTarget(null);
        }}
        subject={skipTarget?.title ?? ""}
        title={t("wave3.recurring.skip")}
        description={t("wave3.recurring.skipConfirm")}
        confirmLabel={t("wave3.recurring.skip")}
        onConfirm={async () => {
          if (skipTarget) await doSkip(skipTarget);
          setSkipTarget(null);
        }}
      />
    </section>
  );
}
