import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  AlertTriangle,
  BatteryLow,
  Cpu,
  Gauge,
  Loader2,
  Pause,
  Play,
  Plus,
  RotateCw,
  Thermometer,
  Trash2,
  X,
  Zap,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { StatusDot } from "@/components/signature/StatusDot";
import { api, ApiError } from "@/lib/api";
import { duration, timestamp } from "@/lib/format";
import { purgeTone } from "@/lib/purgeOutcome";
import { cn } from "@/lib/utils";
import {
  JOB_MODES,
  JOB_MODE_HINT,
  JOB_MODE_LABEL,
  type JobKindInfo,
  type JobMode,
  type JobState,
  type JobView,
  type JobWindow,
  type JobsOverview,
  type PostProdKindSettings,
  type PostProdSettings,
  type WhisperInfo,
} from "@/lib/types";
import { useT, useTN, type TranslationKey, type Translator } from "@/lib/i18n";

// The jobs page is where the user controls the CPU tradeoff this whole tier is
// built around, so the thing it has to make legible is not the queue — it is
// the REASON. A paused job with no explanation reads as a broken job, so every
// row that is not running says what would have to change for it to run, and
// the gate panel above says which of those the machine is asserting right now.

/** How often the page re-reads. The governor samples every five seconds, so a
 *  faster poll would only show the same snapshot twice. */
const POLL_MS = 4000;

/** The one job kind whose history row owns a file on disk.
 *
 *  internal/clipper/worker.go registers the export under this exact name, and
 *  internal/api/jobs.go's removeClipExport returns early for every other kind —
 *  so this is the only kind whose deletion reaches past the row. It is spelled
 *  out here because the delete confirmation has to say what is destroyed, and
 *  claiming a file loss for a transcode that never wrote an export is the same
 *  defect as hiding the one that does happen. */
const CLIP_EXPORT_KIND = "clip.export";

const STATE_TONE: Record<JobState, "live" | "warn" | "down" | "outline" | "default" | "armed"> = {
  running: "live",
  queued: "outline",
  deferred: "warn",
  done: "default",
  failed: "down",
  cancelled: "outline",
};

function errText(err: unknown, fallback: string): string {
  if (err instanceof ApiError || err instanceof Error) return err.message;
  return fallback;
}

/** Minutes past midnight as HH:MM, which is what a time input wants. */
function minutesToClock(m: number): string {
  const clamped = Math.max(0, Math.min(1439, Math.round(m)));
  const h = Math.floor(clamped / 60);
  return `${String(h).padStart(2, "0")}:${String(clamped % 60).padStart(2, "0")}`;
}

function clockToMinutes(s: string): number {
  const [h, m] = s.split(":").map((n) => Number.parseInt(n, 10));
  if (Number.isNaN(h) || Number.isNaN(m)) return 0;
  return h * 60 + m;
}

/** The browser's own zone, offered as the default so nobody has to know that an
 *  empty zone means UTC on the server. Wrapped because a locked-down runtime
 *  can throw here, and a zone we cannot read is UTC — the same answer the
 *  server gives an empty one. Mirrors AutomationPage.browserZone. */
function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

const DAY_LABELS: TranslationKey[] = [
  "jobs.dayInitialSun",
  "jobs.dayInitialMon",
  "jobs.dayInitialTue",
  "jobs.dayInitialWed",
  "jobs.dayInitialThu",
  "jobs.dayInitialFri",
  "jobs.dayInitialSat",
];
const DAY_NAMES: TranslationKey[] = [
  "jobs.daySunday",
  "jobs.dayMonday",
  "jobs.dayTuesday",
  "jobs.dayWednesday",
  "jobs.dayThursday",
  "jobs.dayFriday",
  "jobs.daySaturday",
];
const DAY_SHORT: TranslationKey[] = [
  "auto.weekSun",
  "auto.weekMon",
  "auto.weekTue",
  "auto.weekWed",
  "auto.weekThu",
  "auto.weekFri",
  "auto.weekSat",
];

/** A window may wrap midnight, and a wrapping one belongs to the day its START
 *  falls on. Said out loud in the editor, because 22:00–06:00 on Saturday
 *  running into Sunday morning is exactly the thing people get wrong. */
function windowSummary(t: Translator, w: JobWindow): string {
  const start = minutesToClock(w.startMinutes);
  const end = w.endMinutes >= 1440 ? "24:00" : minutesToClock(w.endMinutes);
  const wraps = w.endMinutes < w.startMinutes;
  const days = w.days?.length
    ? w.days.map((d) => (DAY_SHORT[d] ? t(DAY_SHORT[d]) : String(d))).join(", ")
    : t("jobs.everyDayLower");
  return `${start}–${end}${wraps ? ` ${t("jobs.nextDay")}` : ""}, ${days} ${w.tz || "UTC"}`;
}

export function JobsPage() {
  const t = useT();
  const [view, setView] = useState<JobsOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [purgeOpen, setPurgeOpen] = useState(false);
  // The job whose row delete is waiting on a confirmation, held as the JOB
  // rather than as its id: the dialog has to name what is destroyed, and only
  // the job itself knows whether a file is part of that (see CLIP_EXPORT_KIND).
  const [pendingDelete, setPendingDelete] = useState<JobView | null>(null);
  // A RUNNING job asks first: cancelling it throws away the work already done.
  // A queued job has no progress to lose, so it cancels on the click (A60).
  const [pendingCancel, setPendingCancel] = useState<JobView | null>(null);
  // The policy is edited locally and saved explicitly. Live-saving every
  // keystroke of a CPU ceiling would write a policy nobody meant through the
  // half-typed values on the way there.
  const [draft, setDraft] = useState<PostProdSettings | null>(null);
  const [dirty, setDirty] = useState(false);
  // A ref rather than state: the poll must not restart every time it fires.
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;

  // A streak of failed polls. The first failure is news and toasts; the ones
  // after it are the same news every four seconds, so they are said once, in
  // place, until a poll gets through (A59).
  const failingRef = useRef(false);
  const [connectionLost, setConnectionLost] = useState(false);

  const load = useCallback(async (): Promise<void> => {
    try {
      const next = await api.jobsOverview();
      failingRef.current = false;
      setConnectionLost(false);
      setView(next);
      // An unsaved edit is never clobbered by a poll. The queue's own numbers
      // keep updating; only the form the user is holding is left alone.
      if (!dirtyRef.current) setDraft(next.policy);
    } catch (err) {
      if (!failingRef.current) toast.error(errText(err, t("jobs.couldNotReadTheJob")));
      failingRef.current = true;
      setConnectionLost(true);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(t);
  }, [load]);

  const act = useCallback(
    async (fn: () => Promise<unknown>, ok: string) => {
      setBusy(true);
      try {
        await fn();
        toast.success(ok);
        await load();
      } catch (err) {
        toast.error(errText(err, t("jobs.thatDidNotWork")));
      } finally {
        setBusy(false);
      }
    },
    [load, t],
  );

  /** Purging the history, on its own rather than through `act`.
   *
   *  It went through `act` before, and "nothing was old enough to purge" was
   *  raised as an Error so that `act`'s catch would put it on screen. That is
   *  an exception used as a return channel, and it arrived in the red toast
   *  kept for things that went wrong -- so an operator who asked a question
   *  and got the honest answer "none" was told their action failed. The count
   *  is a RESULT; it is reported as one, in the tone `purgeTone` picks. */
  const purgeHistory = useCallback(async () => {
    setBusy(true);
    try {
      const { purged } = await api.purgeJobs({});
      if (purgeTone(purged) === "info") toast.info(t("jobs.nothingWasOldEnoughTo"));
      else toast.success(t("jobs.historyPurged", { count: purged }));
      await load();
    } catch (err) {
      toast.error(errText(err, t("jobs.thatDidNotWork")));
    } finally {
      setBusy(false);
    }
  }, [load, t]);

  const savePolicy = useCallback(async () => {
    if (!draft) return;
    setBusy(true);
    try {
      const res = await api.putJobPolicy(draft);
      setDirty(false);
      setDraft(res.policy);
      if (res.restartRequired) {
        toast.warning(
          t("jobs.savedTheConcurrencyChangeTakes"),
        );
      } else {
        toast.success(t("jobs.policySaved"));
      }
      await load();
    } catch (err) {
      toast.error(errText(err, t("jobs.couldNotSaveThePolicy")));
    } finally {
      setBusy(false);
    }
  }, [draft, load, t]);

  const patch = useCallback((p: Partial<PostProdSettings>) => {
    setDirty(true);
    setDraft((d) => (d ? { ...d, ...p } : d));
  }, []);

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  // No answer yet from an unreachable server is not the same as "no queue on
  // this server", and must not be said as if it were (A59).
  if (!view && connectionLost) {
    return (
      <div className="p-3">
        <PageHeader title={t("jobs.title")} subtitle={t("jobs.subtitle")} />
        <p role="status" className="text-sm text-warn">
          {t("jobs.connectionLostRetrying")}
        </p>
      </div>
    );
  }

  if (!view?.available) {
    return (
      <div className="p-3">
        <PageHeader
          title={t("jobs.title")}
          subtitle={t("jobs.subtitle")}
        />
        <Card>
          <CardContent className="py-8 text-center text-sm text-muted-foreground">
            {t("jobs.noQueue")}
          </CardContent>
        </Card>
      </div>
    );
  }

  const gates = view.governor?.gates;
  const governed = Boolean(view.governor?.enabled);

  return (
    <div className="p-3">
      <PageHeader
        title={t("jobs.title")}
        subtitle={t("jobs.yieldNote")}
        actions={
          <>
            {view.paused && <Badge variant="warn">{t("jobs.queuePausedBadge")}</Badge>}
            {view.stats.running > 0 && (
              <Badge variant="live">
                <StatusDot tone="live" size="sm" />
                {t("jobs.runningCount", { count: view.stats.running })}
              </Badge>
            )}
            {/* Pause all needs something to pause. Over an empty queue it has no
                target, so it is not offered; a paused queue always keeps its
                Resume all, whatever is in it (A64). */}
            {(view.paused || view.active.length > 0) && (
              <Button
                variant={view.paused ? "default" : "outline"}
                size="sm"
                disabled={busy}
                onClick={() =>
                  act(
                    () => (view.paused ? api.resumeJobs() : api.pauseJobs()),
                    view.paused ? t("jobs.queueResumed") : t("jobs.queuePaused"),
                  )
                }
              >
                {view.paused ? <Play /> : <Pause />}
                {view.paused ? t("jobs.resumeAll") : t("jobs.pauseAll")}
              </Button>
            )}
          </>
        }
      />

      {connectionLost && (
        <p role="status" className="mt-2 text-sm text-warn">
          {t("jobs.connectionLostRetrying")}
        </p>
      )}

      {/* ---- the gates: what the machine is asserting right now ---- */}
      <GatePanel view={view} />

      <Tabs defaultValue="queue" className="mt-3">
        <TabsList>
          <TabsTrigger value="queue">
            {view.active.length > 0
              ? t("jobs.queueTabCount", { count: view.active.length })
              : t("jobs.queueTab")}
          </TabsTrigger>
          <TabsTrigger value="policy">{t("jobs.policy")}</TabsTrigger>
          <TabsTrigger value="history">{t("jobs.history")}</TabsTrigger>
        </TabsList>

        <TabsContent value="queue">
          <Card>
            <CardHeader>
              <CardTitle>{t("jobs.inQueue")}</CardTitle>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              <JobTable
                jobs={view.active}
                empty={t("jobs.nothingQueued")}
                busy={busy}
                onCancel={(id) => {
                  const job = view.active.find((j) => j.id === id);
                  if (job?.state === "running") setPendingCancel(job);
                  else void act(() => api.cancelJob(id), t("jobs.jobCancelled"));
                }}
                onRelease={
                  governed
                    ? (id) =>
                        act(
                          () => api.releaseJob(id),
                          t("jobs.releasedItWillStartAs"),
                        )
                    : undefined
                }
              />
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="policy">
          {draft && (
            <PolicyEditor
              draft={draft}
              kinds={view.kinds}
              dirty={dirty}
              busy={busy}
              onPatch={patch}
              whisper={view.whisper}
              onSave={savePolicy}
              onRevert={() => {
                setDraft(view.policy);
                setDirty(false);
              }}
            />
          )}
        </TabsContent>

        <TabsContent value="history">
          <Card>
            <CardHeader className="flex-row items-center justify-between gap-2">
              <CardTitle>{t("jobs.finished")}</CardTitle>
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => setPurgeOpen(true)}
              >
                <Trash2 />
                {t("jobs.purge")}
              </Button>
              {/* IT DELETES FILES, and it said how many only afterwards.
                  handlePurgeJobs calls removeClipExport for every job it drops,
                  so the button labelled "Purge history" reaches past the
                  history rows and takes the exported clips off disk with them.
                  The count arrived in a toast, which is a report, not a
                  decision -- by the time it is on screen the files are gone.
                  Same dialog as every other destructive action here, and it
                  says what leaves. */}
              <ConfirmDestructive
                open={purgeOpen}
                onOpenChange={setPurgeOpen}
                // No one target: the pool is named by `consequences` below.
                subject={{ unnamed: true }}
                title={t("jobs.purgeTitle")}
                description={t("jobs.purgeDescription")}
                consequencesLabel={t("jobs.purgeRemoves")}
                // Only the rows on screen can be counted here: which of them
                // are old enough to go is the server's retention policy to
                // apply, and a number this page guessed would be worse than
                // none. The description names the rule; this names the pool.
                consequences={
                  view.recent.length > 0
                    ? [{ label: t("jobs.finished"), count: view.recent.length }]
                    : undefined
                }
                confirmLabel={t("jobs.purge")}
                onConfirm={purgeHistory}
              />
            </CardHeader>
            <CardContent className="px-0 pb-0">
              <JobTable
                jobs={view.recent}
                empty={t("jobs.noFinished")}
                busy={busy}
                onRetry={(id) => act(() => api.retryJob(id), t("jobs.jobRearmed"))}
                onDelete={(id) =>
                  setPendingDelete(view.recent.find((j) => j.id === id) ?? null)
                }
              />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* Cancelling a running job loses the work done on it (A60). */}
      <ConfirmDestructive
        open={pendingCancel !== null}
        onOpenChange={(open) => {
          if (!open) setPendingCancel(null);
        }}
        subject={pendingCancel ? (pendingCancel.label ?? pendingCancel.kind) : ""}
        title={t("jobs.cancelRunningTitle")}
        description={t("jobs.cancelRunningDescription")}
        confirmLabel={t("jobs.cancelRunningConfirm")}
        onConfirm={() =>
          pendingCancel
            ? act(() => api.cancelJob(pendingCancel.id), t("jobs.jobCancelled"))
            : undefined
        }
      />

      {/* THE ROW DELETE TAKES THE FILE TOO, and said nothing about it.
          handleDeleteJob (internal/api/jobs.go) calls removeClipExport once the
          row is gone, and a clip export's row is the ONLY reference to its file
          -- the download route is keyed on the job, and the exports directory
          sits outside the rolling buffer's pruning. So the small trash icon in
          the history table was a file delete wearing a list-tidying icon, and
          it went out on the click while the bulk Purge two feet away asked
          first. An operator who learns "deletes ask here" and meets one that
          does not has had their caution trained out of them exactly where it
          mattered.

          The description names the file only for the kind that has one. A
          confirmation that claims a loss which does not happen is the same
          defect as one that hides the loss that does -- and after the third
          "this deletes files" dialog over a transcode that deletes nothing, the
          sentence stops being read at all. */}
      <ConfirmDestructive
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        subject={pendingDelete ? (pendingDelete.label ?? pendingDelete.kind) : ""}
        title={t("jobs.deleteTitle")}
        description={
          pendingDelete?.kind === CLIP_EXPORT_KIND
            ? t("jobs.deleteExportDescription")
            : t("jobs.deleteDescription")
        }
        confirmLabel={t("jobs.deleteConfirm")}
        onConfirm={() =>
          pendingDelete
            ? act(() => api.deleteJob(pendingDelete.id), t("jobs.jobRemoved"))
            : undefined
        }
      />

      {!view.whisper.available && (
        <p className="mt-3 text-sm text-muted-foreground">
          {t("jobs.transcriptionUnavailable", { reason: view.whisper.unavailable ?? "" })}
        </p>
      )}
      {gates && !governed && (
        <p className="mt-3 text-sm text-warn">
          {t("jobs.policyOffNote")}
        </p>
      )}
    </div>
  );
}

// ------------------------------------------------------------------- gates

/** What the machine is asserting right now, and which of it is holding work
 *  back. This is the panel that turns "why is nothing running" into a fact. */
function GatePanel({ view }: { view: JobsOverview }) {
  const t = useT();
  const g = view.governor?.gates;
  const blocked = useMemo(
    () => (view.governor?.verdicts ?? []).filter((v) => !v.start),
    [view.governor],
  );

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-3 py-3">
        <Stat
          labelKey="jobs.running"
          value={view.stats.running}
          tone={view.stats.running > 0 ? "live" : "muted"}
        />
        <Stat labelKey="jobs.queued" value={view.counts.queued ?? 0} />
        <Stat
          labelKey="jobs.deferred"
          value={view.counts.deferred ?? 0}
          tone={(view.counts.deferred ?? 0) > 0 ? "warn" : "muted"}
        />
        <Stat
          labelKey="jobs.failed"
          value={view.counts.failed ?? 0}
          tone={(view.counts.failed ?? 0) > 0 ? "down" : "muted"}
        />
        <Stat labelKey="jobs.completed" value={view.stats.completed} tone="muted" />

        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {g?.ingestLive && (
            <Badge variant="live" title={t("jobs.broadcastGoing")}>
              <StatusDot tone="live" size="sm" />
              {t("jobs.gateIngestLive")}
            </Badge>
          )}
          {/* -1 is "we could not read it", which is not the same as zero and
              must not be shown as a load of nothing. */}
          {g && g.cpuPercent >= 0 && (
            <Badge variant={g.cpuOverCeiling ? "warn" : "outline"} title={t("jobs.hostCpuLoad")}>
              <Cpu className="h-2.5 w-2.5" />
              {g.cpuPercent.toFixed(0)}%
              {/* The reason a job is held is said in words, not left to the
                  tooltip that touch and keyboard users never see (A61). */}
              {g.cpuOverCeiling && <span className="ml-1">{t("jobs.gateCpuOverCeiling")}</span>}
            </Badge>
          )}
          {g?.gpuBusy && (
            <Badge variant="warn" title={t("jobs.gpuGateNote")}>
              <Zap className="h-2.5 w-2.5" />
              {t("jobs.gateGpuBusy")}
            </Badge>
          )}
          {g?.onBattery && (
            <Badge variant="warn">
              <BatteryLow className="h-2.5 w-2.5" />
              {g.power.known && g.power.percent >= 0
                ? t("jobs.gateOnBatteryPct", { pct: g.power.percent.toFixed(0) })
                : t("jobs.gateOnBattery")}
            </Badge>
          )}
          {g?.tooHot && (
            <Badge variant="down" title={t("jobs.machineSafety")}>
              <Thermometer className="h-2.5 w-2.5" />
              {t("jobs.gateTooHot")}
            </Badge>
          )}
          {view.governor?.yielding?.length ? (
            <Badge
              variant="warn"
              title={t("jobs.cannotPause")}
            >
              {t("jobs.gateFinishing", { kinds: view.governor.yielding.join(", ") })}
            </Badge>
          ) : null}
        </div>

        {blocked.length > 0 && (
          <div className="w-full border-t border-border pt-2">
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {blocked.map((v) => (
                <span key={v.kind} className="text-sm text-muted-foreground">
                  <span className="font-mono text-foreground">{v.kind}</span> — {v.reason}
                </span>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// -------------------------------------------------------------- job tables

function JobTable({
  jobs,
  empty,
  busy,
  onCancel,
  onRetry,
  onRelease,
  onDelete,
}: {
  jobs: JobView[];
  empty: string;
  busy: boolean;
  onCancel?: (id: number) => void;
  onRetry?: (id: number) => void;
  onRelease?: (id: number) => void;
  onDelete?: (id: number) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState<number | null>(null);

  if (jobs.length === 0) {
    return (
      <div className="px-3 py-8 text-center text-sm text-muted-foreground">{empty}</div>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("jobs.job")}</TableHead>
          <TableHead>{t("jobs.recording")}</TableHead>
          <TableHead className="w-24">{t("jobs.state")}</TableHead>
          <TableHead className="w-40">{t("jobs.progress")}</TableHead>
          <TableHead>{t("jobs.why")}</TableHead>
          <TableHead className="w-24" />
        </TableRow>
      </TableHeader>
      <TableBody>
        {jobs.map((j) => {
          const expanded = open === j.id;
          const hasDetail = Boolean(j.error) || (j.log?.length ?? 0) > 0;
          return (
            <Fragment key={j.id}>
              <TableRow>
                <TableCell className="text-sm">
                  <button
                    type="button"
                    className={cn(
                      "text-left",
                      hasDetail ? "hover:text-primary" : "cursor-default",
                    )}
                    disabled={!hasDetail}
                    onClick={() => setOpen(expanded ? null : j.id)}
                    aria-expanded={expanded}
                  >
                    <span className="block">{j.label ?? j.kind}</span>
                    <span className="block font-mono text-sm text-muted-foreground">
                      #{j.id}
                      {j.attempts > 1
                        ? ` · ${t("jobs.attempt", { n: j.attempts, max: j.maxAttempts })}`
                        : ""}
                    </span>
                  </button>
                </TableCell>
                <TableCell className="max-w-[16rem] truncate font-mono text-sm text-muted-foreground">
                  {j.recording ?? j.target}
                </TableCell>
                <TableCell>
                  <Badge variant={STATE_TONE[j.state] ?? "outline"}>{j.state}</Badge>
                </TableCell>
                <TableCell>
                  <ProgressCell job={j} />
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {j.error ? (
                    <span className="flex items-start gap-1 text-down">
                      <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                      <span className="line-clamp-2">{j.error}</span>
                    </span>
                  ) : (
                    (j.reason ?? "")
                  )}
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-0.5">
                    {onRelease && j.blocked && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={busy}
                        onClick={() => onRelease(j.id)}
                        aria-label={t("jobs.runNow")}
                        title={t("jobs.runNowTitle")}
                      >
                        <Play />
                      </Button>
                    )}
                    {onRetry && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={busy}
                        onClick={() => onRetry(j.id)}
                        aria-label={t("jobs.retry")}
                        title={t("jobs.retryTitle")}
                      >
                        <RotateCw />
                      </Button>
                    )}
                    {onCancel && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={busy}
                        onClick={() => onCancel(j.id)}
                        aria-label={t("jobs.cancel")}
                        className="hover:text-down"
                      >
                        <X />
                      </Button>
                    )}
                    {onDelete && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={busy}
                        onClick={() => onDelete(j.id)}
                        aria-label={t("jobs.removeFromHistory")}
                        className="hover:text-down"
                      >
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>

              {expanded && (
                <TableRow>
                  <TableCell colSpan={6} className="bg-card-raised/40">
                    <div className="flex flex-col gap-1 py-1.5">
                      {j.error && <p className="text-sm text-down">{j.error}</p>}
                      {(j.log?.length ?? 0) > 0 && (
                        <pre className="max-h-48 overflow-y-auto whitespace-pre-wrap font-mono text-sm leading-relaxed text-muted-foreground">
                          {j.log?.join("\n")}
                        </pre>
                      )}
                      <p className="text-sm text-muted-foreground">
                        {t("jobs.queuedAt", { time: timestamp(j.createdAt) })}
                        {j.startedAt
                          ? ` · ${t("jobs.startedAt", { time: timestamp(j.startedAt) })}`
                          : ""}
                        {j.finishedAt
                          ? ` · ${t("jobs.finishedAt", { time: timestamp(j.finishedAt) })}`
                          : ""}
                      </p>
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
}

function ProgressCell({ job }: { job: JobView }) {
  const t = useT();
  if (job.state === "done") {
    return <span className="text-sm text-muted-foreground">{t("jobs.complete")}</span>;
  }
  if (job.state !== "running") {
    return <span className="text-sm text-muted-foreground">—</span>;
  }
  const pct = Math.round(Math.min(1, Math.max(0, job.progress)) * 100);
  return (
    <div className="flex items-center gap-2">
      <div className="h-1 flex-1 overflow-hidden rounded-full bg-secondary">
        <div className="h-full bg-live transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      <span className="tnum w-8 shrink-0 text-right font-mono text-sm">{pct}%</span>
      {/* The ETA is withheld below a few per cent, where the extrapolation is
          nonsense. A wildly wrong ETA is worse than none. */}
      {job.etaSeconds ? (
        <span className="tnum w-14 shrink-0 text-right font-mono text-sm text-muted-foreground">
          {duration(job.etaSeconds)}
        </span>
      ) : (
        <span className="w-14 shrink-0" />
      )}
    </div>
  );
}

// ------------------------------------------------------------------ policy

function PolicyEditor({
  draft,
  kinds,
  whisper,
  dirty,
  busy,
  onPatch,
  onSave,
  onRevert,
}: {
  draft: PostProdSettings;
  kinds: JobKindInfo[];
  /** What this machine can do about speech to text. The model picker needs it
   *  to offer real choices rather than a hard-coded list that could name a
   *  model this install does not have. */
  whisper: WhisperInfo;
  dirty: boolean;
  busy: boolean;
  onPatch: (p: Partial<PostProdSettings>) => void;
  onSave: () => void;
  onRevert: () => void;
}) {
  const t = useT();
  const tn = useTN();
  /** The per-kind row, created on first edit. A kind with no row inherits the
   *  default, which is why the editor writes one only when the user changes
   *  something rather than materialising five rows on load. */
  const setKind = (kind: string, p: Partial<PostProdKindSettings>) => {
    const rows = draft.kinds ? [...draft.kinds] : [];
    const i = rows.findIndex((k) => k.kind === kind);
    if (i >= 0) rows[i] = { ...rows[i], ...p };
    else rows.push({ kind, ...p });
    onPatch({ kinds: rows });
  };

  const clearKind = (kind: string) =>
    onPatch({ kinds: (draft.kinds ?? []).filter((k) => k.kind !== kind) });

  return (
    <div className="flex flex-col gap-3">
      {/* STICKY, and the only Save on the tab.

          There used to be a second one at the bottom of the right-hand column
          rendered under `!dirty` -- a Save button that existed only while
          there was nothing to save, and was withdrawn the instant there was.
          An operator who had learnt where it lived edited a field and watched
          it disappear. (The poll does not clobber the draft: load() guards on
          `dirtyRef.current`. The defect was the vanishing button alone.)

          Deleting it leaves one Save, and `sticky top-0` is what makes that
          safe: this tab is a long two-column grid, so a bar that only exists
          at the top is a bar an operator editing the last card cannot see. */}
      {dirty && (
        <div className="sticky top-0 z-10 flex items-center justify-end gap-2 rounded-md border border-warn/40 bg-background/95 px-2 py-1.5 backdrop-blur">
          <span className="mr-auto text-sm text-warn">{t("jobs.unsavedChanges")}</span>
          <Button variant="ghost" size="sm" disabled={busy} onClick={onRevert}>
            {t("jobs.revert")}
          </Button>
          <Button size="sm" disabled={busy} onClick={onSave}>
            {t("jobs.savePolicy")}
          </Button>
        </div>
      )}

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {/* ---------- per-kind modes ---------- */}
        <div className="flex flex-col gap-3">
          {kinds.map((k) => {
            const row = draft.kinds?.find((r) => r.kind === k.kind);
            const mode = (row?.mode || draft.defaultMode || "deferred") as JobMode;
            const windows = row?.windows ?? k.windows ?? [];
            return (
              <Card key={k.kind}>
                <CardHeader className="flex-row items-start justify-between gap-2">
                  <div className="min-w-0">
                    <CardTitle>{k.label}</CardTitle>
                    <p className="mt-0.5 text-sm text-muted-foreground">{k.description}</p>
                    {!k.available && (
                      <p className="mt-1 text-sm text-warn">{k.unavailable}</p>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    {row && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => clearKind(k.kind)}
                        title={t("jobs.backToDefault")}
                      >
                        {t("jobs.useDefault")}
                      </Button>
                    )}
                    <Select value={mode} onValueChange={(v) => setKind(k.kind, { mode: v as JobMode })}>
                      <SelectTrigger className="w-40">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {JOB_MODES.map((m) => (
                          <SelectItem key={m} value={m}>
                            {t(JOB_MODE_LABEL[m])}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </CardHeader>
                <CardContent className="flex flex-col gap-2">
                  <p className="text-sm text-muted-foreground">{t(JOB_MODE_HINT[mode])}</p>

                  {mode === "scheduled" && (
                    <WindowEditor
                      idBase={k.kind}
                      windows={windows}
                      onChange={(next) => setKind(k.kind, { windows: next })}
                    />
                  )}

                  <div className="flex flex-wrap items-center gap-x-5 gap-y-2 pt-1">
                    <label className="flex items-center gap-2 text-sm">
                      <Switch
                        checked={row?.usesGpu ?? k.usesGpu}
                        onCheckedChange={(v) => setKind(k.kind, { usesGpu: v })}
                      />
                      {t("jobs.competesWithGpu")}
                    </label>
                    <label className="flex items-center gap-2 text-sm">
                      <Switch
                        checked={row?.ignoreIngest ?? k.ignoreIngest}
                        onCheckedChange={(v) => setKind(k.kind, { ignoreIngest: v })}
                      />
                      {t("jobs.cheapDuringBroadcast")}
                    </label>
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>

        {/* ---------- global controls ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle>{t("jobs.global")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <label className="flex items-center justify-between gap-2 text-sm">
                {t("jobs.resourcePolicy")}
                <Switch
                  checked={draft.enabled}
                  onCheckedChange={(v) => onPatch({ enabled: v })}
                />
              </label>
              <p className="-mt-2 text-sm text-muted-foreground">
                {t("jobs.resourcePolicyOffHint")}
              </p>

              <label className="flex items-center justify-between gap-2 text-sm">
                {t("jobs.yieldToStream")}
                <Switch
                  checked={draft.yieldToStream}
                  onCheckedChange={(v) => onPatch({ yieldToStream: v })}
                />
              </label>

              <div className="flex flex-col gap-1">
                <Label htmlFor="jobs-default-mode">{t("jobs.defaultMode")}</Label>
                <Select
                  value={(draft.defaultMode || "deferred") as JobMode}
                  onValueChange={(v) => onPatch({ defaultMode: v as JobMode })}
                >
                  <SelectTrigger id="jobs-default-mode">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {JOB_MODES.map((m) => (
                      <SelectItem key={m} value={m}>
                        {t(JOB_MODE_LABEL[m])}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <NumberField
                id="jobs-concurrency"
                label={t("jobs.concurrency")}
                hint={t("jobs.howManyJobsMayRun")}
                value={draft.concurrency}
                min={1}
                max={16}
                onChange={(n) => onPatch({ concurrency: n })}
              />
              <NumberField
                id="jobs-cpu-ceiling"
                label={t("jobs.cpuCeiling")}
                hint={t("jobs.nothingNewStartsAboveThis")}
                value={draft.cpuCeilingPercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ cpuCeilingPercent: n })}
              />
              <NumberField
                id="jobs-cpu-resume"
                label={t("jobs.cpuResume")}
                hint={t("jobs.whereTheCeilingReleasesThe")}
                value={draft.cpuResumePercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ cpuResumePercent: n })}
              />
              {/* THE FOUR TIMINGS THE TWO PERCENTAGES ABOVE ARE MEASURED WITH.
                  The ceiling and the resume level say WHERE the gate moves;
                  sustain and settle say how long the load has to stay there
                  before it does, and linger and defer say how long the other
                  two gates hold on afterwards. All four have been in
                  PostProdSettings and in the policy endpoint since the governor
                  landed, and none of them had a control -- so an operator whose
                  transcodes were suspended by a five-second CPU spike could see
                  the ceiling that did it, could edit the ceiling, and could not
                  reach the window that decided the spike counted. The only
                  route was hand-editing settings on disk.

                  0 is NOT "off" for three of the four. normalise() in
                  internal/jobs/governor.go substitutes DefaultCPUSustained,
                  DefaultCPUSettle and DefaultDeferFor for a zero, so those
                  three fields read 0 and behave as 30, 20 and 30 seconds; only
                  ingest linger honours a zero as "release the gate at once".
                  Each hint says so, because a field showing 0 while the machine
                  waits half a minute is a lie an operator can only discover by
                  timing it with a stopwatch. */}
              <NumberField
                id="jobs-cpu-sustained"
                label={t("jobs.cpuSustained")}
                hint={t("jobs.cpuSustainedHint")}
                value={draft.cpuSustainedSeconds}
                min={0}
                max={3600}
                onChange={(n) => onPatch({ cpuSustainedSeconds: n })}
              />
              <NumberField
                id="jobs-cpu-settle"
                label={t("jobs.cpuSettle")}
                hint={t("jobs.cpuSettleHint")}
                value={draft.cpuSettleSeconds}
                min={0}
                max={3600}
                onChange={(n) => onPatch({ cpuSettleSeconds: n })}
              />
              <NumberField
                id="jobs-ingest-linger"
                label={t("jobs.ingestLinger")}
                hint={t("jobs.ingestLingerHint")}
                value={draft.ingestLingerSeconds}
                min={0}
                max={3600}
                onChange={(n) => onPatch({ ingestLingerSeconds: n })}
              />
              <NumberField
                id="jobs-defer-seconds"
                label={t("jobs.deferFor")}
                hint={t("jobs.deferHint")}
                value={draft.deferSeconds}
                min={0}
                max={3600}
                onChange={(n) => onPatch({ deferSeconds: n })}
              />
              <NumberField
                id="jobs-nice"
                label={t("jobs.niceLevel")}
                hint={t("jobs.niceHint")}
                value={draft.niceLevel}
                min={0}
                max={19}
                onChange={(n) => onPatch({ niceLevel: n })}
              />
              <label className="flex items-center justify-between gap-2 text-sm">
                {t("jobs.idleIo")}
                <Switch checked={draft.idleIo} onCheckedChange={(v) => onPatch({ idleIo: v })} />
              </label>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>
                <Gauge className="mr-1 inline h-3 w-3" />
                {t("jobs.machineGates")}
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <label className="flex items-center justify-between gap-2 text-sm">
                {t("jobs.avoidGpu")}
                <Switch
                  checked={draft.avoidGpuWhenStreaming}
                  onCheckedChange={(v) => onPatch({ avoidGpuWhenStreaming: v })}
                />
              </label>
              <label className="flex items-center justify-between gap-2 text-sm">
                {t("jobs.gpuInUse")}
                <Switch checked={draft.gpuBusy} onCheckedChange={(v) => onPatch({ gpuBusy: v })} />
              </label>
              <p className="-mt-2 text-sm text-muted-foreground">
                {t("jobs.gpuInUseHint")}
              </p>
              <NumberField
                id="jobs-battery"
                label={t("jobs.batteryFloor")}
                hint={t("jobs.holdDeferredWorkBackOn")}
                value={draft.batteryFloorPercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ batteryFloorPercent: n })}
              />
              <NumberField
                id="jobs-thermal"
                label={t("jobs.thermalCeiling")}
                hint={t("jobs.stopsEverythingRealtimeIncludedA")}
                value={draft.thermalCeilingC}
                min={0}
                max={120}
                onChange={(n) => onPatch({ thermalCeilingC: n })}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("jobs.transcription")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              <Label htmlFor="jobs-whisper-model">{t("jobs.defaultModel")}</Label>
              <select
                id="jobs-whisper-model"
                value={draft.whisperModel ?? ""}
                onChange={(e) => onPatch({ whisperModel: e.target.value })}
                className="h-8 w-56 rounded border border-border bg-card-raised px-2 text-sm"
              >
                {/* Empty is first and is the default, because the
                    hardware-derived choice is the right answer until an operator
                    has a reason to override it. */}
                <option value="">{t("jobs.automaticModel")}</option>
                {(whisper.models ?? []).map((m: string) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
              <span className="text-sm text-muted-foreground">
                {tn("jobs.defaultModelHint", {
                  model: <strong>{whisper.defaultModel || t("jobs.defaultModelNone")}</strong>,
                })}
              </span>
              {!whisper.available && (
                <span className="text-sm text-warn">
            {t("jobs.whisperMissing")}
                </span>
              )}
              {(whisper.models ?? []).length === 0 && whisper.available && (
                <span className="text-sm text-warn">
            {t("jobs.noModels")}
                </span>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("jobs.historyRetention")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <NumberField
                id="jobs-retain-days"
                label={t("jobs.keepForDays")}
                hint={t("jobs.retainDaysHint")}
                value={draft.retainDays}
                min={0}
                max={3650}
                onChange={(n) => onPatch({ retainDays: n })}
              />
              <NumberField
                id="jobs-retain-jobs"
                label={t("jobs.alwaysKeepNewest")}
                hint={t("jobs.thisManyFinishedJobsSurvive")}
                value={draft.retainJobs}
                min={0}
                max={100000}
                onChange={(n) => onPatch({ retainJobs: n })}
              />
            </CardContent>
          </Card>

        </div>
      </div>
    </div>
  );
}

function NumberField({
  id,
  label,
  hint,
  value,
  min,
  max,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
}) {
  // What is typed is kept as text, so an emptied field stays empty. The old
  // code wrote NaN through as the minimum, so clearing the box to type 70 over
  // 85 left "1" in it mid-edit (A62). On blur or Enter the value is clamped.
  const [text, setText] = useState(String(value));
  // Follows the saved value when it changes from outside, by adjusting state
  // during render rather than in an effect.
  const [seen, setSeen] = useState(value);
  if (value !== seen) {
    setSeen(value);
    setText(String(value));
  }

  const commit = () => {
    const n = Number.parseInt(text, 10);
    if (Number.isNaN(n)) {
      setText(String(value));
      return;
    }
    const clamped = Math.min(max, Math.max(min, n));
    setText(String(clamped));
    if (clamped !== value) onChange(clamped);
  };

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        className="tnum font-mono"
        min={min}
        max={max}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          // A number is passed up as it is typed, as before. Nothing is
          // written for an empty field, so NaN no longer becomes the minimum.
          const n = Number.parseInt(e.target.value, 10);
          if (!Number.isNaN(n)) onChange(n);
        }}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit();
        }}
      />
      {hint && (
        <p className="text-sm text-muted-foreground">
          {hint} <span className="tnum font-mono">({min}–{max})</span>
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------- window editor

function WindowEditor({
  idBase,
  windows,
  onChange,
}: {
  /** Makes the zone datalist's id unique. One of these editors is rendered per
   *  scheduled kind, and two <datalist> elements sharing an id is the kind of
   *  invalid HTML that works until it does not. */
  idBase: string;
  windows: JobWindow[];
  onChange: (next: JobWindow[]) => void;
}) {
  const t = useT();
  const tz = browserZone();
  const zoneListId = `jobs-window-tz-${idBase}`;

  const set = (i: number, p: Partial<JobWindow>) =>
    onChange(windows.map((w, n) => (n === i ? { ...w, ...p } : w)));

  const toggleDay = (i: number, day: number) => {
    const days = windows[i].days ?? [];
    // NUMERIC COMPARATOR, because Array.prototype.sort has none by default: it
    // coerces to string, so [1, 10, 2] stays in that order. Weekdays are 0-6
    // and single digits happen to sort identically either way, so nothing was
    // visibly wrong -- which is exactly why this is worth fixing rather than
    // suppressing. The bug is latent until the day somebody stores a value
    // above 9 here, and then it is a sorting fault nobody looks for.
    const next = days.includes(day)
      ? days.filter((d) => d !== day)
      : [...days, day].sort((a, b) => a - b);
    set(i, { days: next });
  };

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-card-raised/40 p-2">
      {windows.length === 0 && (
        <p className="text-sm text-warn">
          {t("jobs.noWindows")}
        </p>
      )}
      {windows.map((w, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <Input
            type="time"
            className="tnum h-7 w-24 font-mono text-sm"
            value={minutesToClock(w.startMinutes)}
            onChange={(e) => set(i, { startMinutes: clockToMinutes(e.target.value) })}
            aria-label={t("jobs.windowStart")}
          />
          <span className="text-sm text-muted-foreground">{t("jobs.windowTo")}</span>
          <Input
            type="time"
            className="tnum h-7 w-24 font-mono text-sm"
            value={minutesToClock(w.endMinutes >= 1440 ? 1439 : w.endMinutes)}
            onChange={(e) => set(i, { endMinutes: clockToMinutes(e.target.value) })}
            aria-label={t("jobs.windowEnd")}
          />
          <div className="flex gap-0.5">
            {DAY_LABELS.map((d, day) => {
              const on = (w.days ?? []).includes(day);
              return (
                <button
                  key={day}
                  type="button"
                  title={t(DAY_NAMES[day])}
                  aria-pressed={on}
                  onClick={() => toggleDay(i, day)}
                  className={cn(
                    "h-6 w-6 rounded text-sm font-semibold transition-colors",
                    on
                      ? "bg-primary-dim text-foreground"
                      : "text-muted-foreground hover:bg-accent",
                  )}
                >
                  {t(d)}
                </button>
              );
            })}
          </div>
          {/* THE ZONE IS PART OF THE WINDOW, and it was captured once and then
              printed for ever.
              A window is created carrying whatever zone the CREATING BROWSER
              was in -- see the Add window handler below, which is right, because
              an operator setting 02:00 means 02:00 where they are. What was
              missing is the second half: nothing could ever change it again.
              An operator who added an overnight window from a hotel in Tokyo
              came home to a policy that runs transcodes at 02:00 JST, saw the
              zone stated in the summary line, and had no way to correct it
              short of deleting the window and rebuilding its days by hand.
              A free-text IANA name rather than a picker, because the server
              takes any zone time.LoadLocation knows and rejects the rest with a
              message that names the string it could not load -- the same
              contract, and the same control, as the schedule editor on the
              automation page. The datalist offers the two answers that are
              nearly always wanted without shutting out the third. */}
          <Input
            list={zoneListId}
            className="h-7 w-44 font-mono text-sm"
            value={w.tz ?? ""}
            spellCheck={false}
            placeholder="UTC"
            onChange={(e) => set(i, { tz: e.target.value })}
            aria-label={t("jobs.windowZone")}
            title={t("jobs.windowZoneHint")}
          />
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onChange(windows.filter((_, n) => n !== i))}
            aria-label={t("jobs.removeWindow")}
            className="hover:text-down"
          >
            <X />
          </Button>
          <span className="w-full text-sm text-muted-foreground">{windowSummary(t, w)}</span>
        </div>
      ))}
      {/* One list for every row in this editor, which is why the id carries the
          kind: the suggestions are the same for all of them. */}
      <datalist id={zoneListId}>
        <option value="UTC" />
        <option value={tz} />
      </datalist>
      {windows.length > 0 && (
        <p className="text-sm text-muted-foreground">{t("jobs.windowZoneHint")}</p>
      )}
      <Button
        variant="outline"
        size="sm"
        className="self-start"
        onClick={() =>
          // The browser's zone, not the server's: the operator means 02:00
          // where they are. Empty days means every day, which is what somebody
          // adding an overnight window almost always wants.
          onChange([...windows, { tz, startMinutes: 120, endMinutes: 360, days: [] }])
        }
      >
        <Plus />
        {t("jobs.addWindow")}
      </Button>
    </div>
  );
}
