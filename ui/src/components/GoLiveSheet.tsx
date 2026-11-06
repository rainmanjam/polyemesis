import { causeLabel } from "@/lib/failureRemedy";
import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
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
import { destinationLanded, processRunning } from "@/lib/liveness";
import { unconfirmedVerticalPairs } from "@/lib/youtubeVertical";
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

/** How long a press waits for its destinations to settle before it gives the
 *  button back, counted from the last start request.
 *
 *  The server has no start timeout to mirror: the supervisor marks a process
 *  running the moment it spawns. What does bound a start that never takes is
 *  the destination hold: three quick rejections in a row (each inside
 *  failreason.QuickWindow, 15 s) with 1 s and 2 s of backoff between them is
 *  about 48 s before a refused destination is held and reads Failed. A minute
 *  covers that with room to spare, and a row still Starting after it is shown
 *  as still starting rather than locking the button. */
export const SETTLE_TIMEOUT_MS = 60_000;
/** How long the final count stays on the button once every row has settled. */
export const FINAL_HOLD_MS = 1_000;
/** Between the first word and the last, the live region speaks at most this
 *  often: a count that ticks every few hundred milliseconds is noise. */
export const ANNOUNCE_EVERY_MS = 2_000;

type Tally = { live: number; failed: number; settled: number; total: number };

/** A press: the rows it set out to start, fixed at the click, with what each
 *  one's process looked like then. Walking while the start requests go out,
 *  settling until every row is live or failed (or the wait runs out), then
 *  done: terminal, with the count frozen while it is held. */
type Run = {
  targets: GoLiveRow[];
  /** Each target's process identity at the click: see processIdentity. */
  before: Map<number, string>;
  phase: "walking" | "settling" | "done";
  timedOut: boolean;
  final?: Tally;
};

/** Which run of a process a status describes. startedAt and restarts change
 *  with every spawn, so a status with the identity seen at the click is the
 *  one an earlier run left, and says nothing about this press. */
function processIdentity(d: DestStatus | undefined): string {
  const p = d?.process;
  return `${p?.state ?? "none"}|${p?.startedAt ?? ""}|${p?.restarts ?? ""}`;
}

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
  onRetry,
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
  /** Revives a destination whose PROCESS failed -- held after quick
   *  rejections, or out of restarts. The card's Retry, the same restart. A
   *  start would be a no-op on a destination that is already enabled. */
  onRetry: (id: DestinationId) => Promise<void>;
  onFinished: () => void;
  /** Gap between starts. The server paces its own bulk start at two seconds. */
  paceMs?: number;
}>) {
  const t = useT();
  // The current press. Its targets are fixed at the click, not re-derived from
  // the live list, so the button does not recount "4 ready, 3 ready…" downward
  // and read as if one click started only one destination. What it counts is
  // how many of THOSE have actually come on, from the live status.
  const [run, setRun] = useState<Run | null>(null);
  // The start requests are in flight. Only this blocks closing the sheet: a
  // press waiting for a row to settle must not lock the operator in.
  const running = run?.phase === "walking";
  const [attempted, setAttempted] = useState<Set<number>>(new Set());
  const [errors, setErrors] = useState<Map<number, string>>(new Map());
  // Which source the sheet is scoped to, by name; null is every source. This is
  // the per-source Go live: the same sheet, the same press, fewer rows.
  const [scope, setScope] = useState<string | null>(null);

  // Bumped by every press; unmount sets it to -1. With the rows a press began
  // on, it lets a walk that outlives its sheet (unmounted, or reopened on a new
  // snapshot) write nothing back into it.
  const pressRef = useRef(0);
  const rowsRef = useRef(rows);
  useEffect(() => {
    rowsRef.current = rows;
  }, [rows]);
  useEffect(() => {
    const press = pressRef;
    return () => {
      press.current = -1;
    };
  }, []);

  // A new snapshot is a new sheet. The parent takes a fresh rows array each
  // time it opens, so a refusal from an earlier press must not still be on
  // screen the next time the operator opens it. Reset during render (React's
  // own recommended pattern for state that follows a prop), not in an effect.
  const [session, setSession] = useState(rows);
  if (rows !== session) {
    setSession(rows);
    setAttempted(new Set());
    setErrors(new Map());
    setRun(null);
    setScope(null);
  }
  // Closing the sheet ends the wait for the rows to settle; the starts already
  // sent stand. Not mid-walk: the requests are still going out.
  if (!open && run !== null && run.phase !== "walking") setRun(null);

  // The source chips, in first-seen order. Only offered when there is a choice.
  const sourceNames = [...new Set(rows.map((r) => r.sourceName).filter((n): n is string => n !== null))];
  const scoped = scope === null ? rows : rows.filter((r) => r.sourceName === scope);

  const liveOf = (id: number) => live.find((d) => (d.id as unknown as number) === id);

  // The one-time Live Control Room step for the YouTube pairs being started (#7).
  // Dual stream cannot be added once a stream has started, so the reminder stays
  // on this sheet until the step is confirmed on the card.
  const pendingPairs = unconfirmedVerticalPairs(
    live.filter((d) => rows.some((r) => r.id === d.id)),
  );

  const progressOf = (row: GoLiveRow): Progress => {
    const id = row.id as unknown as number;
    if (!row.ready) return "blocked";
    if (errors.has(id)) return "failed";
    const proc = liveOf(id)?.process;
    const state = proc?.state;
    // Live is lib/liveness's destinationLanded: running, media moved, not
    // stalled. The supervisor says running the moment FFmpeg spawns, so a
    // start the platform is about to refuse reads running for a moment; until
    // media has gone out it is still starting. An enabled destination with no
    // process, or one that is retrying, is not live either.
    if (destinationLanded(proc)) return "live";
    if (state === "failed") return "failed";
    if (attempted.has(id) || state === "starting" || state === "reconnecting" || processRunning(proc)) return "starting";
    return "pending";
  };

  const progress = scoped.map((r) => ({ row: r, p: progressOf(r) }));
  const count = (p: Progress) => progress.filter((x) => x.p === p).length;
  const readyNow = planStarts(scoped).filter((r) => !liveOf(r.id as unknown as number)?.enabled);

  // A3: one click, on the row that failed, through the door that does
  // something. A refused START never made a process, so it is started again;
  // a failed PROCESS is restarted, which is what lifts a #20 hold.
  const [retrying, setRetrying] = useState<Set<number>>(new Set());
  const retry = async (id: number) => {
    setRetrying((prev) => new Set(prev).add(id));
    const refusedStart = errors.has(id);
    setErrors((prev) => {
      const next = new Map(prev);
      next.delete(id);
      return next;
    });
    try {
      if (refusedStart) {
        setAttempted((prev) => new Set(prev).add(id));
        await onStart(id as unknown as DestinationId);
      } else {
        await onRetry(id as unknown as DestinationId);
      }
    } catch (e) {
      setErrors((prev) => new Map(prev).set(id, e instanceof Error ? e.message : String(e)));
    } finally {
      setRetrying((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  };

  // The press, counted from the live status of the rows it set out to start.
  // The same progressOf the row chips use, so the button and the rows agree.
  // A row counts only on a status from a process started since the click.
  const tally = ((): Tally => {
    if (!run) return { live: 0, failed: 0, settled: 0, total: 0 };
    if (run.final) return run.final;
    let live = 0;
    let failed = 0;
    for (const row of run.targets) {
      const id = row.id as unknown as number;
      // Live is live whenever it is seen: output moving cannot be left over.
      // A refused request settles the row at once. Otherwise a status settles
      // it only if it comes from a process started since the click: one with
      // the identity seen at the click (a Failed left by an earlier run, or a
      // row the walk has not reached yet) says nothing about this press.
      const p = progressOf(row);
      if (p !== "live" && !errors.has(id) && processIdentity(liveOf(id)) === run.before.get(id)) continue;
      if (p === "live") live++;
      else if (p === "failed" || p === "blocked") failed++;
    }
    return { live, failed, settled: live + failed, total: run.targets.length };
  })();
  const allSettled = run !== null && tally.settled === tally.total;
  // Over when every target is live or failed, and not before the last request
  // has gone out; or when the wait for a stuck row runs out. Terminal: the
  // count is frozen here, so a row that drops during the hold does not reopen
  // the press or restart its timeout. Set during render, as the session reset
  // above is.
  // Not on the render that resets the sheet (a new snapshot, or closed): the
  // reset above has already dropped this run, and the update is functional so
  // it can never bring one back.
  if (rows === session && open && run !== null && run.phase === "settling" && (allSettled || run.timedOut)) {
    const final = tally;
    setRun((current) => (current?.phase === "settling" ? { ...current, phase: "done", final } : current));
  }
  const phase = run?.phase ?? null;

  // A row that never settles does not keep the button forever.
  useEffect(() => {
    if (phase !== "settling") return;
    const timer = setTimeout(
      () => setRun((r) => (r && r.phase === "settling" ? { ...r, timedOut: true } : r)),
      SETTLE_TIMEOUT_MS,
    );
    return () => clearTimeout(timer);
  }, [phase]);

  // The final count stands for a moment, then the button is the button again.
  useEffect(() => {
    if (phase !== "done") return;
    const timer = setTimeout(() => setRun(null), FINAL_HOLD_MS);
    return () => clearTimeout(timer);
  }, [phase]);

  const failedPart =
    tally.failed > 0
      ? t(tally.failed === 1 ? "i18nSweep.failedCountOne" : "i18nSweep.failedCount", { count: tally.failed })
      : null;
  const still = tally.total - tally.settled;
  const stillPart =
    run?.timedOut && still > 0
      ? t(still === 1 ? "golive.stillStartingOne" : "golive.stillStarting", { count: still })
      : null;
  const progressLabel = run
    ? [
        t(phase === "done" ? "golive.progressDone" : "golive.progressStarting", {
          live: tally.live,
          total: tally.total,
        }),
        failedPart,
        stillPart,
      ]
        .filter((part): part is string => part !== null)
        .join(" · ")
    : "";

  // What the live region says: the first word and the final count at once,
  // anything between at most every ANNOUNCE_EVERY_MS, and always the latest.
  const [announced, setAnnounced] = useState("");
  const spokeAt = useRef<number | null>(null);
  const final = phase === "done";
  useEffect(() => {
    if (progressLabel === "") {
      spokeAt.current = null;
      return;
    }
    const speak = () => {
      spokeAt.current = Date.now();
      setAnnounced(progressLabel);
    };
    const wait = spokeAt.current === null || final ? 0 : spokeAt.current + ANNOUNCE_EVERY_MS - Date.now();
    if (wait <= 0) {
      speak();
      return;
    }
    const timer = setTimeout(speak, wait);
    return () => clearTimeout(timer);
  }, [progressLabel, final]);

  const go = async () => {
    const press = ++pressRef.current;
    const sheet = rows;
    const current = () => pressRef.current === press && rowsRef.current === sheet;
    const targets = readyNow;
    setErrors(new Map());
    setAttempted(new Set());
    setAnnounced("");
    setRun({
      targets,
      before: new Map(targets.map((r) => [r.id as unknown as number, processIdentity(liveOf(r.id as unknown as number))])),
      phase: "walking",
      timedOut: false,
    });
    await runPacedStarts(
      targets.map((r) => r.id as unknown as number),
      async (id) => {
        if (current()) setAttempted((prev) => new Set(prev).add(id));
        await onStart(id as unknown as DestinationId);
      },
      paceMs,
      (id, message) => {
        if (current()) setErrors((prev) => new Map(prev).set(id, message));
      },
    );
    if (!current()) return;
    setRun((r) => (r ? { ...r, phase: "settling" } : r));
    onFinished();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !running && onOpenChange(o)}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("golive.title")}</DialogTitle>
          <DialogDescription>{t("golive.intro")}</DialogDescription>
        </DialogHeader>

        {pendingPairs > 0 && (
          <p
            className="rounded-md border border-warn/40 bg-warn-dim px-3 py-2 text-sm text-warn"
            data-testid="golive-vertical-reminder"
          >
            {t(pendingPairs === 1 ? "wave3.vertical.goLiveReminderOne" : "wave3.vertical.goLiveReminder", { count: pendingPairs })}
          </p>
        )}

        {youtubeAtRisk > 0 && (
          <p className="rounded-md border border-warn/40 bg-warn-dim px-3 py-2 text-sm text-warn">
            {t(youtubeAtRisk === 1 ? "golive.youtubeWarnOne" : "golive.youtubeWarn", { count: youtubeAtRisk })}
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
              {/* Wraps rather than truncates: the source is what the operator
                  confirms before a press, and "Main Programme — St…" cannot be
                  confirmed (A90). The tooltip carries the full name on hover. */}
              <span className="min-w-0 break-words text-muted-foreground" title={row.sourceName ?? undefined}>
                {row.sourceName ?? "—"}
              </span>
              <span>
                {/* A row that is already on air is not READY: it is not waiting
                    for anything, and it is not counted in the start button. Its
                    LIVE mark is the progress column's chip, so this cell is left
                    empty rather than saying it twice (A8). */}
                {p === "live" ? null : row.ready ? (
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
                {/* The classified cause (#20) of a process that failed after
                    it started, which the start request itself never sees. */}
                {p === "failed" &&
                  !errors.has(row.id as unknown as number) &&
                  liveOf(row.id as unknown as number)?.reason && (
                    <StatusReason
                      className="text-xs"
                      reason={causeLabel(t, liveOf(row.id as unknown as number)!.reason!.cause)}
                      step={t("foundation.status.step.destination", { platform: row.platform })}
                      asOf={liveOf(row.id as unknown as number)!.reason!.at}
                    />
                  )}
                {p === "failed" && (
                  <span>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={running || retrying.has(row.id as unknown as number)}
                      aria-label={t("reason.retryNamed", { name: row.name })}
                      onClick={() => void retry(row.id as unknown as number)}
                    >
                      <RefreshCw /> {t("reason.retry")}
                    </Button>
                  </span>
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
              disabled={run !== null || readyNow.length === 0}
              onClick={() => void go()}
            >
              {run
                ? progressLabel
                : t(readyNow.length === 1 ? "golive.goCountOne" : "golive.goCount", { count: readyNow.length })}
            </Button>
          </div>
          {/* Always mounted, empty when idle: a live region that appears with
              its first message is often not announced, and a disabled button's
              changing label is not announced at all. */}
          <div role="status" aria-live="polite" className="sr-only" data-testid="golive-progress">
            {run ? announced : ""}
          </div>
          {/* Not while a press runs: the starts coming on empty readyNow
              before the press is over, and "nothing is ready" beside "Starting…
              3 of 3 live" reads as a failure. */}
          {run === null && readyNow.length === 0 && (
            <p className="w-full text-sm text-muted-foreground">{t("golive.nothingReady")}</p>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
