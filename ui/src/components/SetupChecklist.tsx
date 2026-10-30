import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SecretCode } from "@/components/SecretCode";
import { TourReplayButton } from "@/components/Tour";
import { urlCarriesCredential } from "@/lib/credential-url";
import type { Checklist, ChecklistStepId, ChecklistStepState } from "@/lib/setupChecklist";
import { useT, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* The first-run checklist for an install that has a source and no destination.
 *
 * It sits where the single "no destinations yet" sentence used to be, and it
 * does not replace the tour: the tour is the walkthrough, this is the state.
 * Everything it ticks comes from the Checklist it is handed (lib/setupChecklist),
 * so this component only draws what the derivation already decided. */

const BADGE: Record<
  ChecklistStepState,
  { key: TranslationKey; variant: "live" | "default" | "armed" | "outline" }
> = {
  done: { key: "firstRun.badgeDone", variant: "live" },
  next: { key: "firstRun.badgeNext", variant: "default" },
  waiting: { key: "firstRun.badgeWaiting", variant: "armed" },
  todo: { key: "firstRun.badgeTodo", variant: "outline" },
  locked: { key: "firstRun.badgeLocked", variant: "outline" },
};

const REASON: Record<NonNullable<Checklist["reason"]>, TranslationKey> = {
  needSourceAndDestination: "firstRun.needSourceAndDestination",
  needDestination: "firstRun.needDestination",
  needSource: "firstRun.needSource",
};

export function SetupChecklist({
  checklist,
  ingestUrl,
  onCopyIngest,
  onAddDestination,
}: Readonly<{
  checklist: Checklist;
  ingestUrl: string;
  onCopyIngest: () => void;
  onAddDestination: () => void;
}>) {
  const t = useT();
  const stateOf = (id: ChecklistStepId) => checklist.steps.find((s) => s.id === id)!.state;

  const titles: Record<ChecklistStepId, TranslationKey> = {
    1: "firstRun.stepSource",
    2: "firstRun.stepEncoder",
    3: "firstRun.stepSignal",
    4: "firstRun.stepDestination",
    5: "firstRun.stepLive",
  };

  const body = (id: ChecklistStepId): string | null => {
    const state = stateOf(id);
    switch (id) {
      case 1:
        return state === "done" ? t("firstRun.stepSourceDone") : null;
      case 2:
        return t("firstRun.stepEncoderBody");
      case 3:
        return state === "done"
          ? t("firstRun.stepSignalDone")
          : state === "waiting"
            ? t("firstRun.stepSignalWaiting")
            : null;
      case 4:
        return t("firstRun.stepDestinationBody");
      case 5:
        return t("firstRun.stepLiveBody");
    }
  };

  return (
    <Card data-testid="setup-checklist">
      <CardHeader className="flex-row items-center justify-between gap-3">
        <CardTitle>{t("firstRun.title")}</CardTitle>
        <div className="flex items-center gap-2">
          <span
            className="h-1.5 w-28 overflow-hidden rounded-full bg-muted"
            role="progressbar"
            aria-label={t("firstRun.progress", { done: checklist.done, total: 5 })}
            aria-valuemin={0}
            aria-valuemax={5}
            aria-valuenow={checklist.done}
          >
            <span
              className="block h-full rounded-full bg-live"
              style={{ width: `${(checklist.done / 5) * 100}%` }}
            />
          </span>
          <span className="tnum font-mono text-tiny text-muted-foreground">
            {t("firstRun.progress", { done: checklist.done, total: 5 })}
          </span>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-0">
        <ol className="flex flex-col">
          {checklist.steps.map(({ id, state }) => {
            const badge = BADGE[state];
            const note = body(id);
            return (
              <li
                key={id}
                data-state={state}
                className={cn(
                  "grid grid-cols-[28px_1fr_auto] items-start gap-3 border-t border-border py-3 first:border-t-0",
                  state === "next" && "rounded-md border border-border-strong bg-card-raised px-3",
                )}
              >
                <span
                  className={cn(
                    "flex h-7 w-7 items-center justify-center rounded-full border text-[13px] font-semibold",
                    state === "done" && "border-live bg-live-dim text-live",
                    state === "next" && "border-primary text-foreground",
                    (state === "todo" || state === "waiting") && "border-border-strong text-muted-foreground",
                    state === "locked" && "border-dashed border-border-strong text-muted-foreground",
                  )}
                  aria-hidden="true"
                >
                  {state === "done" ? <Check className="h-3.5 w-3.5" /> : id}
                </span>

                <div className="flex min-w-0 flex-col gap-1">
                  <span
                    className={cn(
                      "text-sm font-semibold",
                      state === "done" && "font-medium text-muted-foreground",
                      state === "locked" && "text-muted-foreground",
                    )}
                  >
                    {t(titles[id])}
                  </span>
                  {note && <span className="text-sm text-muted-foreground">{note}</span>}

                  {id === 2 && state !== "done" && (
                    <div className="mt-1 flex flex-col gap-1">
                      <span className="text-micro uppercase tracking-wide text-muted-foreground">
                        {t("firstRun.ingestHeading")}
                      </span>
                      <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                      {urlCarriesCredential(ingestUrl) ? (
                        <SecretCode value={ingestUrl} />
                      ) : (
                        <code className="min-w-0 flex-1 truncate font-mono text-tiny">{ingestUrl}</code>
                      )}
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={onCopyIngest}
                        aria-label={t("dash.copyIngestUrl")}
                      >
                        <Copy />
                      </Button>
                      </div>
                    </div>
                  )}

                  {id === 4 && state !== "done" && (
                    <div className="mt-1">
                      <Button size="sm" variant="outline" onClick={onAddDestination}>
                        {t("firstRun.addDestination")}
                      </Button>
                    </div>
                  )}

                  {id === 5 && (
                    <div className="mt-1 flex flex-col items-start gap-1.5">
                      <Button size="sm" disabled={state === "locked"}>
                        {t("golive.trigger")}
                      </Button>
                      {state === "locked" && checklist.reason && (
                        <span className="text-sm font-medium text-warn">
                          {t(REASON[checklist.reason])}
                        </span>
                      )}
                    </div>
                  )}
                </div>

                <Badge variant={badge.variant} className="mt-0.5">
                  {t(badge.key)}
                </Badge>
              </li>
            );
          })}
        </ol>

        <div className="flex justify-end border-t border-border pt-3">
          <TourReplayButton labelKey="firstRun.tour" />
        </div>
      </CardContent>
    </Card>
  );
}
