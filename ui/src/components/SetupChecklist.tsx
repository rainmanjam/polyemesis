import { Check, Copy } from "lucide-react";
import { Link } from "react-router";
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
  addresses = [],
  onCopyAddress,
}: Readonly<{
  checklist: Checklist;
  ingestUrl: string;
  onCopyIngest: () => void;
  onAddDestination: () => void;
  /** The source's publish rows, host filled in (A1): its address, and in RTMP
   *  mode a `streamKey` row, offered as a copy and never displayed. */
  addresses?: readonly { proto: string; url: string }[];
  onCopyAddress?: (address: { proto: string; url: string }) => void;
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

  const signalState = stateOf(3);

  return (
    // Two columns from lg up (A1): the steps on the left, and on the right the
    // addresses the encoder needs with the live signal reading beside them, so
    // step 2 and step 3 are read in the same glance as they are done.
    <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)]">
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
          <span className="tnum font-mono text-xs text-muted-foreground">
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

                  {/* The addresses themselves sit in the card beside the steps
                      (IngestAddresses). The step keeps one copy button per
                      protocol, so an encoder that wants SRT is not handed an
                      RTMP address to edit. */}
                  {id === 2 && state === "next" && addresses.length > 0 && onCopyAddress && (
                    <div className="mt-1 flex flex-wrap gap-1.5">
                      {addresses.map((a, i) => (
                        <Button
                          key={a.proto}
                          size="sm"
                          variant={i === 0 ? "default" : "outline"}
                          onClick={() => onCopyAddress(a)}
                        >
                          <Copy />{" "}
                          {a.proto === "streamKey"
                            ? t("firstRun.copyStreamKey")
                            : t("firstRun.copyAddress", { proto: a.proto.toUpperCase() })}
                        </Button>
                      ))}
                    </div>
                  )}

                  {id === 1 && state === "next" && (
                    <div className="mt-1">
                      <Button size="sm" variant="outline" asChild>
                        <Link to="/sources">{t("empty.goToSources")}</Link>
                      </Button>
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

        <div className="flex items-center justify-between gap-3 border-t border-border pt-3">
          <span className="text-sm text-muted-foreground">{t("firstRun.tourHint")}</span>
          <TourReplayButton labelKey="firstRun.tour" />
        </div>
      </CardContent>
    </Card>

    {checklist.steps[0].state === "done" && (
      <IngestAddresses
        addresses={addresses}
        ingestUrl={ingestUrl}
        signal={signalState === "done"}
        onCopyIngest={onCopyIngest}
        onCopyAddress={onCopyAddress}
      />
    )}
    </div>
  );
}

/** The encoder's half of setup (A1): every publish address with its own copy
 *  button, and whether anything is arriving on them. Shown once a source
 *  exists; before that these would be addresses to nothing.
 *
 *  A stream key, or a URL that carries one, is masked behind SecretCode like
 *  everywhere else in the console: a first-run screen is the one most likely
 *  to be shared or screen-recorded while someone helps set it up. */
function IngestAddresses({
  addresses,
  ingestUrl,
  signal,
  onCopyIngest,
  onCopyAddress,
}: Readonly<{
  addresses: readonly { proto: string; url: string }[];
  ingestUrl: string;
  signal: boolean;
  onCopyIngest: () => void;
  onCopyAddress?: (address: { proto: string; url: string }) => void;
}>) {
  const t = useT();
  // The per-protocol rows when the source has them; the install's ingest URL
  // otherwise, so the card is never empty on an older server.
  const rows = addresses.length > 0 ? addresses : ingestUrl ? [{ proto: "", url: ingestUrl }] : [];
  return (
    <Card data-testid="setup-addresses">
      <CardHeader className="flex-row items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <CardTitle>{t("firstRun.addressesTitle")}</CardTitle>
          <span className="text-sm text-muted-foreground">{t("firstRun.addressesBody")}</span>
        </div>
        <Badge variant={signal ? "live" : "armed"}>
          {signal ? t("firstRun.signalBadge") : t("firstRun.noSignalBadge")}
        </Badge>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {rows.map((a) => {
          const secret = a.proto === "streamKey" || urlCarriesCredential(a.url);
          const label = a.proto === "streamKey" ? t("sources.streamKey") : a.proto ? a.proto.toUpperCase() : t("firstRun.ingestHeading");
          return (
            <div key={a.proto || "ingest"} className="flex flex-col gap-1">
              <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{label}</span>
              <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                {secret ? (
                  <SecretCode value={a.url} />
                ) : (
                  <code className="min-w-0 flex-1 truncate font-mono text-xs">{a.url}</code>
                )}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => (a.proto && onCopyAddress ? onCopyAddress(a) : onCopyIngest())}
                  aria-label={a.proto ? t("firstRun.copyRow", { label }) : t("dash.copyIngestUrl")}
                >
                  <Copy />
                </Button>
              </div>
            </div>
          );
        })}

        <div
          className={cn(
            "flex items-start gap-3 rounded-md border px-3 py-2.5",
            signal ? "border-live/40 bg-live-dim" : "border-dashed border-border-strong",
          )}
          role="status"
        >
          <span className="relative mt-1.5 flex h-2.5 w-2.5 shrink-0" aria-hidden="true">
            {!signal && (
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-armed opacity-60 motion-reduce:animate-none" />
            )}
            <span className={cn("relative inline-flex h-2.5 w-2.5 rounded-full", signal ? "bg-live" : "bg-armed")} />
          </span>
          <div className="flex flex-col gap-0.5">
            <span className="text-sm font-semibold">
              {signal ? t("firstRun.stepSignalDone") : t("firstRun.waitingTitle")}
            </span>
            {!signal && <span className="text-sm text-muted-foreground">{t("firstRun.waitingBody")}</span>}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
