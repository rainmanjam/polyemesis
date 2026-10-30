import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { planStarts, runPacedStarts, type GoLiveRow } from "@/lib/goLive";
import { TrackSummary } from "@/components/signature/TrackRows";
import { StatusReason } from "@/components/signature/StatusReason";
import type { DestStatus, DestinationId } from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* The Go live sheet: the start laid out before anything fires.
 *
 * Its rows are FROZEN when it opens (the parent passes a snapshot), so the list
 * and the count an operator reads are the list and the count that the press
 * acts on. The press re-checks each planned destination against the live list
 * before it starts it, so a destination that came on in the meantime is never
 * sent a second start. */

type Progress = "live" | "starting" | "failed" | "pending" | "blocked";

const CHIP: Record<Progress, { key: TranslationKey; variant: "live" | "armed" | "down" | "warn" | "outline" }> = {
  live: { key: "golive.live", variant: "live" },
  starting: { key: "golive.starting", variant: "armed" },
  failed: { key: "golive.failed", variant: "down" },
  pending: { key: "golive.pending", variant: "outline" },
  blocked: { key: "golive.blocked", variant: "warn" },
};

export function GoLiveSheet({
  open,
  onOpenChange,
  rows,
  live,
  youtubeAtRisk,
  onStart,
  onFinished,
  paceMs = 2000,
}: Readonly<{
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Snapshot taken when the sheet opened. Not re-derived while it is open. */
  rows: GoLiveRow[];
  /** The current destination list, for what each row is doing right now. */
  live: readonly DestStatus[];
  youtubeAtRisk: number;
  onStart: (id: DestinationId) => Promise<void>;
  onFinished: () => void;
  /** Gap between starts. The server paces its own bulk start at two seconds. */
  paceMs?: number;
}>) {
  const t = useT();
  const [running, setRunning] = useState(false);
  const [attempted, setAttempted] = useState<Set<number>>(new Set());
  const [errors, setErrors] = useState<Map<number, string>>(new Map());
  // Which source the sheet is scoped to, by name; null is every source. This is
  // the per-source Go live: the same sheet, the same press, fewer rows.
  const [scope, setScope] = useState<string | null>(null);

  // A new snapshot is a new sheet. The parent takes a fresh rows array each
  // time it opens, so a refusal from an earlier press must not still be on
  // screen the next time the operator opens it. Reset during render (React's
  // own recommended pattern for state that follows a prop), not in an effect.
  const [session, setSession] = useState(rows);
  if (rows !== session) {
    setSession(rows);
    setAttempted(new Set());
    setErrors(new Map());
    setRunning(false);
    setScope(null);
  }

  // The source chips, in first-seen order. Only offered when there is a choice.
  const sourceNames = [...new Set(rows.map((r) => r.sourceName).filter((n): n is string => n !== null))];
  const scoped = scope === null ? rows : rows.filter((r) => r.sourceName === scope);

  const liveOf = (id: number) => live.find((d) => (d.id as unknown as number) === id);

  const progressOf = (row: GoLiveRow): Progress => {
    const id = row.id as unknown as number;
    if (!row.ready) return "blocked";
    if (errors.has(id)) return "failed";
    const state = liveOf(id)?.process?.state;
    // Live means the process is running, and nothing else. An enabled
    // destination with no process, or one that is retrying or stalled, is not
    // live, and must not be drawn as if it were.
    if (state === "running") return "live";
    if (state === "failed") return "failed";
    if (attempted.has(id) || state === "starting" || state === "reconnecting") return "starting";
    return "pending";
  };

  const progress = scoped.map((r) => ({ row: r, p: progressOf(r) }));
  const count = (p: Progress) => progress.filter((x) => x.p === p).length;
  const readyNow = planStarts(scoped).filter((r) => !liveOf(r.id as unknown as number)?.enabled);

  const go = async () => {
    setRunning(true);
    setErrors(new Map());
    setAttempted(new Set());
    const ids = readyNow.map((r) => r.id as unknown as number);
    await runPacedStarts(
      ids,
      async (id) => {
        setAttempted((prev) => new Set(prev).add(id));
        await onStart(id as unknown as DestinationId);
      },
      paceMs,
      (id, message) => setErrors((prev) => new Map(prev).set(id, message)),
    );
    setRunning(false);
    onFinished();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !running && onOpenChange(o)}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("golive.title")}</DialogTitle>
          <DialogDescription>{t("golive.intro")}</DialogDescription>
        </DialogHeader>

        {youtubeAtRisk > 0 && (
          <p className="rounded-md border border-warn/40 bg-warn-dim px-3 py-2 text-sm text-warn">
            {t("golive.youtubeWarn", { count: youtubeAtRisk })}
          </p>
        )}

        {sourceNames.length > 1 && (
          <div role="group" aria-label={t("golive.colSource")} className="flex flex-wrap gap-1.5">
            {[null, ...sourceNames].map((name) => {
              const n = name === null ? rows.length : rows.filter((r) => r.sourceName === name).length;
              return (
                <Button
                  key={name ?? "*"}
                  size="sm"
                  variant={scope === name ? "secondary" : "outline"}
                  aria-pressed={scope === name}
                  disabled={running}
                  onClick={() => setScope(name)}
                >
                  {name ?? t("golive.allSources")}
                  <span className="font-mono text-xs text-muted-foreground">{n}</span>
                </Button>
              );
            })}
          </div>
        )}

        <div className="flex flex-col divide-y divide-border rounded-md border border-border">
          <div className="grid grid-cols-[1.4fr_1fr_1fr_auto_1fr] gap-3 px-3 py-2 text-xs uppercase tracking-wide text-muted-foreground">
            <span>{t("golive.colDestination")}</span>
            <span>{t("golive.colSource")}</span>
            <span>{t("golive.colReadiness")}</span>
            <span>{t("golive.colAudio")}</span>
            <span>{t("golive.colProgress")}</span>
          </div>
          {progress.map(({ row, p }) => (
            <div
              key={row.id as unknown as number}
              data-row
              className="grid grid-cols-[1.4fr_1fr_1fr_auto_1fr] items-center gap-3 px-3 py-2 text-sm"
            >
              <span className="min-w-0">
                <span className="block truncate font-medium">{row.name}</span>
                <span className="block font-mono text-xs text-muted-foreground">{row.platform}</span>
              </span>
              <span className="truncate text-muted-foreground">{row.sourceName ?? "—"}</span>
              <span>
                {row.ready ? (
                  <Badge variant="armed">{t("golive.ready")}</Badge>
                ) : (
                  <Badge variant="warn">{t("golive.blockedNoSource")}</Badge>
                )}
              </span>
              {/* What this platform will hear, from the live list: the snapshot
                  carries no tracks, and routing can change while the sheet is
                  open. */}
              <TrackSummary tracks={liveOf(row.id as unknown as number)?.tracks ?? null} />
              <span className={cn("flex flex-col gap-0.5", p === "failed" && "text-down")}>
                <span>
                  <Badge variant={CHIP[p].variant}>{t(CHIP[p].key)}</Badge>
                </span>
                {p === "failed" && errors.has(row.id as unknown as number) && (
                  <StatusReason
                    className="text-xs"
                    reason={errors.get(row.id as unknown as number) ?? ""}
                    step={t("foundation.status.step.destination", { platform: row.platform })}
                    asOf={null}
                  />
                )}
              </span>
            </div>
          ))}
        </div>

        <DialogFooter className="flex-wrap items-center gap-3 sm:justify-between">
          <span className="font-mono text-xs text-muted-foreground">
            {t("golive.summary", {
              live: count("live"),
              starting: count("starting"),
              failed: count("failed"),
              blocked: count("blocked"),
            })}
          </span>
          <div className="flex items-center gap-2">
            <Button variant="outline" disabled={running} onClick={() => onOpenChange(false)}>
              {t("golive.cancel")}
            </Button>
            <Button
              disabled={running || readyNow.length === 0}
              onClick={() => void go()}
            >
              {t("golive.goCount", { count: readyNow.length })}
            </Button>
          </div>
          {readyNow.length === 0 && (
            <p className="w-full text-sm text-muted-foreground">{t("golive.nothingReady")}</p>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
