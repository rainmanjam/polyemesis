import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { SetupChecklist } from "@/components/SetupChecklist";
import { useIngestLive } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import { copyToClipboard } from "@/lib/clipboard";
import { failedRead, mayClaim, okRead, pendingRead, type ReadState } from "@/lib/readState";
import { readyToStream, setupChecklist, type ChecklistStepId } from "@/lib/setupChecklist";
import {
  overviewTiles,
  readSecretKeyAck,
  type OverviewReads,
  type OverviewTile,
} from "@/lib/settingsOverview";
import type { Settings, SystemInfo } from "@/lib/types";
import { cn } from "@/lib/utils";

/* THE SETTINGS LANDING PAGE (settings-council R3, R5).
 *
 * Two answers, in the order they are asked. "Can I stream yet?" is the
 * readiness strip and the same five-step checklist the Dashboard shows, drawn
 * by the same component from the same derivation (lib/setupChecklist), so the
 * two pages can never disagree about what is left. "Is everything else
 * configured?" is one tile per area, each a one-line summary built from reads
 * the settings tabs already make -- no endpoint exists for this page.
 *
 * EVERY TILE IS A ReadState. A read that failed says "could not read", never
 * "0 connected": an Overview that lies about an empty install is worse than no
 * Overview, because it is the page people trust to be a summary. */

const STEP_TITLE: Record<ChecklistStepId, TranslationKey> = {
  1: "firstRun.stepSource",
  2: "firstRun.stepEncoder",
  3: "firstRun.stepSignal",
  4: "firstRun.stepDestination",
  5: "firstRun.stepLive",
};

export function SettingsOverview({
  settings,
  system,
  sourceCount,
  defaultSourceName,
  showAdvanced,
  onOpen,
}: Readonly<{
  settings: Settings;
  system: SystemInfo | null;
  sourceCount: number | null;
  defaultSourceName: string | null;
  showAdvanced: boolean;
  onOpen: (tab: string) => void;
}>) {
  const t = useT();
  const navigate = useNavigate();
  const signal = useIngestLive();
  const [reads, setReads] = useState<OverviewReads>({
    destinations: pendingRead(),
    creds: pendingRead(),
    accounts: pendingRead(),
    guides: pendingRead(),
    overlays: pendingRead(),
    obs: pendingRead(),
    tls: pendingRead(),
    tokens: pendingRead(),
  });

  useEffect(() => {
    // One setter per read, each with an explicit failure branch: see the
    // ReadState note above.
    const put =
      <K extends keyof OverviewReads>(key: K) =>
      (value: OverviewReads[K]) =>
        setReads((prev) => ({ ...prev, [key]: value }));
    const read = <K extends keyof OverviewReads, V>(
      key: K,
      p: Promise<V>,
      pick: (v: V) => OverviewReads[K] extends ReadState<infer U> ? U : never,
    ) => {
      p.then((v) => put(key)(okRead(pick(v)) as OverviewReads[K])).catch(() =>
        put(key)(failedRead() as OverviewReads[K]),
      );
    };
    read("destinations", api.listDestinations(), (rows) => rows.length);
    read("creds", api.listCreds(), (rows) => rows);
    read("accounts", api.listAccounts(), (rows) => rows);
    read("guides", api.platformGuides(), (rows) => rows);
    read("overlays", api.listOverlays(), (r) => r.overlays.length);
    read("obs", api.getObs(), (r) => r);
    read("tls", api.tlsStatus(), (r) => r);
    read("tokens", api.listTokens(), (rows) => rows.length);
  }, []);

  const destinations = mayClaim(reads.destinations) ? reads.destinations.value : null;
  // Drawn only when both counts are known: a checklist built on a guess would
  // tick or untick steps the server never reported.
  const checklist =
    destinations === null ? null : setupChecklist({ sourceCount, signal, destinations });
  const nextStep = checklist?.steps.find((step) => step.state === "next");
  // The strip and the checklist below it read the same steps (readyToStream),
  // so "Ready to stream" never sits above "2 of 5 done".
  const ready = checklist !== null && readyToStream(checklist);

  const tiles = overviewTiles({
    settings,
    sourceCount,
    defaultSourceName,
    secretKeyBackedUp: readSecretKeyAck(),
    reads,
  }, t);

  return (
    <div className="flex flex-col gap-3" data-testid="settings-overview">
      {/* THE READINESS STRIP: one sentence, and the one next action. */}
      <div
        role="status"
        className={cn(
          "flex flex-wrap items-center gap-2 rounded-md border px-3 py-2",
          ready ? "border-live/40 bg-live-dim" : "border-border bg-card",
        )}
      >
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {t("settingsUx.readyTitle")}
        </span>
        <span className="mr-auto text-sm">
          {checklist === null
            ? readFailedText(reads.destinations, t)
            : ready
              ? t("settingsUx.readyYes")
              : t("settingsUx.readyNotYet", { done: checklist.done, total: 5 }) +
                (nextStep ? " " + t("settingsUx.readyNext", { step: t(STEP_TITLE[nextStep.id]) }) : "")}
        </span>
      </div>

      {checklist && (
        <SetupChecklist
          checklist={checklist}
          ingestUrl={system?.ingestUrl ?? ""}
          onCopyIngest={() => {
            if (system?.ingestUrl) void copyToClipboard(t, system.ingestUrl, t("firstRun.ingestHeading"));
          }}
          // The destination dialog lives on the Dashboard, where this same
          // checklist opens it in place. Sending the operator there is one
          // click; a second copy of the dialog here would be a second place for
          // the two to drift apart.
          onAddDestination={() => navigate("/")}
          onGoLive={() => navigate("/")}
        />
      )}

      <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {tiles.map((tile) => (
          <OverviewTileCard key={tile.tab} tile={tile} onOpen={onOpen} />
        ))}
      </ul>

      {!showAdvanced && (
        <p className="text-sm text-muted-foreground">{t("settingsUx.advancedOffNote")}</p>
      )}
    </div>
  );
}

function readFailedText(state: ReadState<unknown>, t: (k: TranslationKey) => string): string {
  // The destinations read is the one the checklist waits on.
  return state.kind === "failed" ? t("settingsUx.tileCouldNotRead") : t("settingsUx.reading");
}

function OverviewTileCard({ tile, onOpen }: { tile: OverviewTile; onOpen: (tab: string) => void }) {
  const t = useT();
  return (
    <li>
      <Card className="h-full">
        <CardContent className="flex h-full flex-col gap-1.5 py-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {t(tile.title)}
            </span>
            {tile.attention && (
              <span className="flex items-center gap-1 text-sm text-warn">
                <span aria-hidden className="h-2 w-2 rounded-full bg-warn" />
                {t("settingsUx.needsAttention")}
              </span>
            )}
          </div>
          {/* Keyed by position, not by text: two reads still pending give two
              identical "Reading…" lines, and a text key made them collide, so
              React kept a stale node and the settled tile showed four lines
              from a three-line array. The lines are a fixed-order list, rebuilt
              whole on every render, so the index is the stable identity. */}
          {tile.lines.map((line, i) => (
            <span key={i} className="text-sm first-of-type:font-medium">
              {line}
            </span>
          ))}
          <Button
            size="sm"
            variant="ghost"
            className="mt-auto w-fit"
            onClick={() => onOpen(tile.tab)}
            aria-label={t("settingsUx.openArea", { area: t(tile.title) })}
          >
            {t("settingsUx.open")} <ChevronRight aria-hidden />
          </Button>
        </CardContent>
      </Card>
    </li>
  );
}
