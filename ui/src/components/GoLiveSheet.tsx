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

  const liveOf = (id: number) => live.find((d) => (d.id as unknown as number) === id);

  const progressOf = (row: GoLiveRow): Progress => {
    const id = row.id as unknown as number;
    if (!row.ready) return "blocked";
    if (errors.has(id)) return "failed";
    const d = liveOf(id);
    const state = d?.process?.state;
    if (state === "failed") return "failed";
    if (d?.enabled && state === "running") return "live";
    if (attempted.has(id) || (d?.enabled && state === "starting")) return "starting";
    if (d?.enabled) return "live";
    return "pending";
  };

  const progress = rows.map((r) => ({ row: r, p: progressOf(r) }));
  const count = (p: Progress) => progress.filter((x) => x.p === p).length;
  const readyNow = planStarts(rows).filter((r) => !liveOf(r.id as unknown as number)?.enabled);

  const go = async () => {
    setRunning(true);
    setErrors(new Map());
    setAttempted(new Set());
    const ids = readyNow.map((r) => r.id as unknown as number);
    const failed = await runPacedStarts(
      ids,
      async (id) => {
        setAttempted((prev) => new Set(prev).add(id));
        await onStart(id as unknown as DestinationId);
      },
      paceMs,
    );
    setErrors(failed);
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

        <div className="flex flex-col divide-y divide-border rounded-md border border-border">
          <div className="grid grid-cols-[1.4fr_1fr_1fr_1fr] gap-3 px-3 py-2 text-micro uppercase tracking-wide text-muted-foreground">
            <span>{t("golive.colDestination")}</span>
            <span>{t("golive.colSource")}</span>
            <span>{t("golive.colReadiness")}</span>
            <span>{t("golive.colProgress")}</span>
          </div>
          {progress.map(({ row, p }) => (
            <div
              key={row.id as unknown as number}
              data-row
              className="grid grid-cols-[1.4fr_1fr_1fr_1fr] items-center gap-3 px-3 py-2 text-sm"
            >
              <span className="min-w-0">
                <span className="block truncate font-medium">{row.name}</span>
                <span className="block font-mono text-micro text-muted-foreground">{row.platform}</span>
              </span>
              <span className="truncate text-muted-foreground">{row.sourceName ?? "—"}</span>
              <span>
                {row.ready ? (
                  <Badge variant="armed">{t("golive.ready")}</Badge>
                ) : (
                  <Badge variant="warn">{t("golive.blockedNoSource")}</Badge>
                )}
              </span>
              <span className={cn("flex flex-col gap-0.5", p === "failed" && "text-down")}>
                <span>
                  <Badge variant={CHIP[p].variant}>{t(CHIP[p].key)}</Badge>
                </span>
                {p === "failed" && errors.has(row.id as unknown as number) && (
                  <span className="text-micro">{errors.get(row.id as unknown as number)}</span>
                )}
              </span>
            </div>
          ))}
        </div>

        <DialogFooter className="flex-wrap items-center gap-3 sm:justify-between">
          <span className="font-mono text-micro text-muted-foreground">
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
