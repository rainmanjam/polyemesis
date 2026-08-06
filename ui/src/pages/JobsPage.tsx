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
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { StatusDot } from "@/components/signature/StatusDot";
import { api, ApiError } from "@/lib/api";
import { duration, timestamp } from "@/lib/format";
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
import { useT } from "@/lib/i18n";

// The jobs page is where the user controls the CPU tradeoff this whole tier is
// built around, so the thing it has to make legible is not the queue — it is
// the REASON. A paused job with no explanation reads as a broken job, so every
// row that is not running says what would have to change for it to run, and
// the gate panel above says which of those the machine is asserting right now.

/** How often the page re-reads. The governor samples every five seconds, so a
 *  faster poll would only show the same snapshot twice. */
const POLL_MS = 4000;

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

const DAY_LABELS = ["S", "M", "T", "W", "T", "F", "S"];
const DAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

/** A window may wrap midnight, and a wrapping one belongs to the day its START
 *  falls on. Said out loud in the editor, because 22:00–06:00 on Saturday
 *  running into Sunday morning is exactly the thing people get wrong. */
function windowSummary(w: JobWindow): string {
  const start = minutesToClock(w.startMinutes);
  const end = w.endMinutes >= 1440 ? "24:00" : minutesToClock(w.endMinutes);
  const wraps = w.endMinutes < w.startMinutes;
  const days = w.days?.length
    ? w.days.map((d) => DAY_NAMES[d]?.slice(0, 3)).join(", ")
    : "every day";
  return `${start}–${end}${wraps ? " (next day)" : ""}, ${days}${w.tz ? ` ${w.tz}` : " UTC"}`;
}

export function JobsPage() {
  const t = useT();
  const [view, setView] = useState<JobsOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  // The policy is edited locally and saved explicitly. Live-saving every
  // keystroke of a CPU ceiling would write a policy nobody meant through the
  // half-typed values on the way there.
  const [draft, setDraft] = useState<PostProdSettings | null>(null);
  const [dirty, setDirty] = useState(false);
  // A ref rather than state: the poll must not restart every time it fires.
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;

  const load = useCallback(async (): Promise<void> => {
    try {
      const next = await api.jobsOverview();
      setView(next);
      // An unsaved edit is never clobbered by a poll. The queue's own numbers
      // keep updating; only the form the user is holding is left alone.
      if (!dirtyRef.current) setDraft(next.policy);
    } catch (err) {
      toast.error(errText(err, "Could not read the job queue."));
    } finally {
      setLoading(false);
    }
  }, []);

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
        toast.error(errText(err, "That did not work."));
      } finally {
        setBusy(false);
      }
    },
    [load],
  );

  const savePolicy = useCallback(async () => {
    if (!draft) return;
    setBusy(true);
    try {
      const res = await api.putJobPolicy(draft);
      setDirty(false);
      setDraft(res.policy);
      if (res.restartRequired) {
        toast.warning(
          "Saved. The concurrency change takes effect when the server restarts — the queue fixes that when it starts.",
        );
      } else {
        toast.success(t("jobs.policySaved"));
      }
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not save the policy."));
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

  if (!view?.available) {
    return (
      <div className="p-3">
        <PageHeader
          title={t("jobs.title")}
          subtitle={t("jobs.subtitle")}
        />
        <Card>
          <CardContent className="py-8 text-center text-[12px] text-muted-foreground">
            No background job queue is running on this server. Post-production
            work cannot be queued until one is.
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
            {view.paused && <Badge variant="warn">queue paused</Badge>}
            {view.stats.running > 0 && (
              <Badge variant="live">
                <StatusDot tone="live" size="sm" />
                {view.stats.running} running
              </Badge>
            )}
            <Button
              variant={view.paused ? "default" : "outline"}
              size="sm"
              disabled={busy}
              onClick={() =>
                act(
                  () => (view.paused ? api.resumeJobs() : api.pauseJobs()),
                  view.paused ? "Queue resumed." : "Queue paused.",
                )
              }
            >
              {view.paused ? <Play /> : <Pause />}
              {view.paused ? "Resume all" : "Pause all"}
            </Button>
          </>
        }
      />

      {/* ---- the gates: what the machine is asserting right now ---- */}
      <GatePanel view={view} />

      <Tabs defaultValue="queue" className="mt-3">
        <TabsList>
          <TabsTrigger value="queue">
            Queue{view.active.length > 0 ? ` (${view.active.length})` : ""}
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
                onCancel={(id) => act(() => api.cancelJob(id), "Job cancelled.")}
                onRelease={
                  governed
                    ? (id) =>
                        act(
                          () => api.releaseJob(id),
                          "Released. It will start as soon as a slot is free.",
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
                onClick={() =>
                  act(async () => {
                    const { purged } = await api.purgeJobs({});
                    if (purged === 0) {
                      throw new Error(
                        "Nothing was old enough to purge. Lower the retention in Policy first.",
                      );
                    }
                  }, "History purged.")
                }
              >
                <Trash2 />
                Purge history
              </Button>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              <JobTable
                jobs={view.recent}
                empty={t("jobs.noFinished")}
                busy={busy}
                onRetry={(id) => act(() => api.retryJob(id), "Job re-armed.")}
                onDelete={(id) => act(() => api.deleteJob(id), "Job removed.")}
              />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {!view.whisper.available && (
        <p className="mt-3 text-[11px] text-muted-foreground">
          Transcription is unavailable on this machine: {view.whisper.unavailable}.
          Everything else still runs — an optional tool being missing is never a
          reason to stop.
        </p>
      )}
      {gates && !governed && (
        <p className="mt-3 text-[11px] text-warn">
          The resource policy is switched off. Jobs still queue and still run;
          they simply stop yielding to the live stream.
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
          label={t("jobs.running")}
          value={view.stats.running}
          tone={view.stats.running > 0 ? "live" : "muted"}
        />
        <Stat label={t("jobs.queued")} value={view.counts.queued ?? 0} />
        <Stat
          label={t("jobs.deferred")}
          value={view.counts.deferred ?? 0}
          tone={(view.counts.deferred ?? 0) > 0 ? "warn" : "muted"}
        />
        <Stat
          label={t("jobs.failed")}
          value={view.counts.failed ?? 0}
          tone={(view.counts.failed ?? 0) > 0 ? "down" : "muted"}
        />
        <Stat label={t("jobs.completed")} value={view.stats.completed} tone="muted" />

        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {g?.ingestLive && (
            <Badge variant="live" title={t("jobs.broadcastGoing")}>
              <StatusDot tone="live" size="sm" />
              ingest live
            </Badge>
          )}
          {/* -1 is "we could not read it", which is not the same as zero and
              must not be shown as a load of nothing. */}
          {g && g.cpuPercent >= 0 && (
            <Badge variant={g.cpuOverCeiling ? "warn" : "outline"} title={t("jobs.hostCpuLoad")}>
              <Cpu className="h-2.5 w-2.5" />
              {g.cpuPercent.toFixed(0)}%
            </Badge>
          )}
          {g?.gpuBusy && (
            <Badge variant="warn" title={t("jobs.gpuGateNote")}>
              <Zap className="h-2.5 w-2.5" />
              gpu busy
            </Badge>
          )}
          {g?.onBattery && (
            <Badge variant="warn">
              <BatteryLow className="h-2.5 w-2.5" />
              on battery
              {g.power.known && g.power.percent >= 0 ? ` ${g.power.percent.toFixed(0)}%` : ""}
            </Badge>
          )}
          {g?.tooHot && (
            <Badge variant="down" title={t("jobs.machineSafety")}>
              <Thermometer className="h-2.5 w-2.5" />
              too hot
            </Badge>
          )}
          {view.governor?.yielding?.length ? (
            <Badge
              variant="warn"
              title={t("jobs.cannotPause")}
            >
              finishing: {view.governor.yielding.join(", ")}
            </Badge>
          ) : null}
        </div>

        {blocked.length > 0 && (
          <div className="w-full border-t border-border pt-2">
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {blocked.map((v) => (
                <span key={v.kind} className="text-[11px] text-muted-foreground">
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
      <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">{empty}</div>
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
                <TableCell className="text-[11px]">
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
                    <span className="block font-mono text-[10px] text-muted-foreground">
                      #{j.id}
                      {j.attempts > 1 ? ` · attempt ${j.attempts}/${j.maxAttempts}` : ""}
                    </span>
                  </button>
                </TableCell>
                <TableCell className="max-w-[16rem] truncate font-mono text-[11px] text-muted-foreground">
                  {j.recording ?? j.target}
                </TableCell>
                <TableCell>
                  <Badge variant={STATE_TONE[j.state] ?? "outline"}>{j.state}</Badge>
                </TableCell>
                <TableCell>
                  <ProgressCell job={j} />
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground">
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
                      {j.error && <p className="text-[11px] text-down">{j.error}</p>}
                      {(j.log?.length ?? 0) > 0 && (
                        <pre className="max-h-48 overflow-y-auto whitespace-pre-wrap font-mono text-[10px] leading-relaxed text-muted-foreground">
                          {j.log?.join("\n")}
                        </pre>
                      )}
                      <p className="text-[10px] text-muted-foreground">
                        queued {timestamp(j.createdAt)}
                        {j.startedAt ? ` · started ${timestamp(j.startedAt)}` : ""}
                        {j.finishedAt ? ` · finished ${timestamp(j.finishedAt)}` : ""}
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
  if (job.state === "done") {
    return <span className="text-[11px] text-muted-foreground">complete</span>;
  }
  if (job.state !== "running") {
    return <span className="text-[11px] text-muted-foreground">—</span>;
  }
  const pct = Math.round(Math.min(1, Math.max(0, job.progress)) * 100);
  return (
    <div className="flex items-center gap-2">
      <div className="h-1 flex-1 overflow-hidden rounded-full bg-secondary">
        <div className="h-full bg-live transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      <span className="tnum w-8 shrink-0 text-right font-mono text-[10px]">{pct}%</span>
      {/* The ETA is withheld below a few per cent, where the extrapolation is
          nonsense. A wildly wrong ETA is worse than none. */}
      {job.etaSeconds ? (
        <span className="tnum w-14 shrink-0 text-right font-mono text-[10px] text-muted-foreground">
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
      {dirty && (
        <div className="flex items-center justify-end gap-2">
          <span className="mr-auto text-[11px] text-warn">{t("jobs.unsavedChanges")}</span>
          <Button variant="ghost" size="sm" disabled={busy} onClick={onRevert}>
            Revert
          </Button>
          <Button size="sm" disabled={busy} onClick={onSave}>
            Save policy
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
                    <p className="mt-0.5 text-[11px] text-muted-foreground">{k.description}</p>
                    {!k.available && (
                      <p className="mt-1 text-[11px] text-warn">{k.unavailable}</p>
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
                        Use default
                      </Button>
                    )}
                    <Select value={mode} onValueChange={(v) => setKind(k.kind, { mode: v as JobMode })}>
                      <SelectTrigger className="w-40">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {JOB_MODES.map((m) => (
                          <SelectItem key={m} value={m}>
                            {JOB_MODE_LABEL[m]}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </CardHeader>
                <CardContent className="flex flex-col gap-2">
                  <p className="text-[11px] text-muted-foreground">{JOB_MODE_HINT[mode]}</p>

                  {mode === "scheduled" && (
                    <WindowEditor
                      windows={windows}
                      onChange={(next) => setKind(k.kind, { windows: next })}
                    />
                  )}

                  <div className="flex flex-wrap items-center gap-x-5 gap-y-2 pt-1">
                    <label className="flex items-center gap-2 text-[11px]">
                      <Switch
                        checked={row?.usesGpu ?? k.usesGpu}
                        onCheckedChange={(v) => setKind(k.kind, { usesGpu: v })}
                      />
                      Competes with the GPU encoder
                    </label>
                    <label className="flex items-center gap-2 text-[11px]">
                      <Switch
                        checked={row?.ignoreIngest ?? k.ignoreIngest}
                        onCheckedChange={(v) => setKind(k.kind, { ignoreIngest: v })}
                      />
                      Cheap enough to run during a broadcast
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
              <label className="flex items-center justify-between gap-2 text-[12px]">
                Resource policy
                <Switch
                  checked={draft.enabled}
                  onCheckedChange={(v) => onPatch({ enabled: v })}
                />
              </label>
              <p className="-mt-2 text-[10px] text-muted-foreground">
                Off makes the governor inert: work still queues and still runs,
                it simply stops yielding.
              </p>

              <label className="flex items-center justify-between gap-2 text-[12px]">
                Yield to the live stream
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
                        {JOB_MODE_LABEL[m]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <NumberField
                id="jobs-concurrency"
                label={t("jobs.concurrency")}
                hint="How many jobs may run at once. One, because a second transcode buys throughput nobody asked for at a risk nobody accepted."
                value={draft.concurrency}
                min={1}
                max={16}
                onChange={(n) => onPatch({ concurrency: n })}
              />
              <NumberField
                id="jobs-cpu-ceiling"
                label={t("jobs.cpuCeiling")}
                hint="Nothing new starts above this. 0 disables the gate."
                value={draft.cpuCeilingPercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ cpuCeilingPercent: n })}
              />
              <NumberField
                id="jobs-cpu-resume"
                label={t("jobs.cpuResume")}
                hint="Where the ceiling releases. The gap is the hysteresis that stops the gate oscillating."
                value={draft.cpuResumePercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ cpuResumePercent: n })}
              />
              <NumberField
                id="jobs-nice"
                label={t("jobs.niceLevel")}
                hint="OS priority every heavy child starts at, 0–19. This applies whatever else the policy says."
                value={draft.niceLevel}
                min={0}
                max={19}
                onChange={(n) => onPatch({ niceLevel: n })}
              />
              <label className="flex items-center justify-between gap-2 text-[12px]">
                Idle IO priority
                <Switch checked={draft.idleIo} onCheckedChange={(v) => onPatch({ idleIo: v })} />
              </label>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>
                <Gauge className="mr-1 inline h-3 w-3" />
                Machine gates
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <label className="flex items-center justify-between gap-2 text-[12px]">
                Avoid the GPU while streaming
                <Switch
                  checked={draft.avoidGpuWhenStreaming}
                  onCheckedChange={(v) => onPatch({ avoidGpuWhenStreaming: v })}
                />
              </label>
              <label className="flex items-center justify-between gap-2 text-[12px]">
                The GPU is in use by streaming
                <Switch checked={draft.gpuBusy} onCheckedChange={(v) => onPatch({ gpuBusy: v })} />
              </label>
              <p className="-mt-2 text-[10px] text-muted-foreground">
                A manual switch because GPU contention is close to undetectable
                on every platform we run on, and guessing "free" is the guess
                that hurts the broadcast.
              </p>
              <NumberField
                id="jobs-battery"
                label={t("jobs.batteryFloor")}
                hint="Hold deferred work back on a discharging laptop below this. 0 disables it."
                value={draft.batteryFloorPercent}
                min={0}
                max={100}
                onChange={(n) => onPatch({ batteryFloorPercent: n })}
              />
              <NumberField
                id="jobs-thermal"
                label={t("jobs.thermalCeiling")}
                hint="Stops everything, realtime included: a CPU that is throttling has already begun degrading the stream. 0 disables it."
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
                className="h-8 w-56 rounded border border-border bg-card-raised px-2 text-[12px]"
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
              <span className="text-[10px] text-muted-foreground">
                What a transcribe job uses when it does not name a model. Bigger models are more
                accurate, slower, and want more memory &mdash; and this machine currently picks{" "}
                <strong>{whisper.defaultModel || "nothing, because whisper.cpp is missing"}</strong>{" "}
                on its own.
              </span>
              {!whisper.available && (
                <span className="text-[10px] text-warn">
            {t("jobs.whisperMissing")}
                </span>
              )}
              {(whisper.models ?? []).length === 0 && whisper.available && (
                <span className="text-[10px] text-warn">
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
                hint="0 keeps finished jobs forever."
                value={draft.retainDays}
                min={0}
                max={3650}
                onChange={(n) => onPatch({ retainDays: n })}
              />
              <NumberField
                id="jobs-retain-jobs"
                label={t("jobs.alwaysKeepNewest")}
                hint="This many finished jobs survive whatever their age."
                value={draft.retainJobs}
                min={0}
                max={100000}
                onChange={(n) => onPatch({ retainJobs: n })}
              />
            </CardContent>
          </Card>

          {!dirty && (
            <Button variant="outline" size="sm" disabled={busy} onClick={onSave}>
              Save policy
            </Button>
          )}
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
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        className="tnum font-mono"
        min={min}
        max={max}
        value={value}
        onChange={(e) => {
          const n = Number.parseInt(e.target.value, 10);
          onChange(Number.isNaN(n) ? min : n);
        }}
      />
      {hint && <p className="text-[10px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

// ---------------------------------------------------------- window editor

function WindowEditor({
  windows,
  onChange,
}: {
  windows: JobWindow[];
  onChange: (next: JobWindow[]) => void;
}) {
  const t = useT();
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

  const set = (i: number, p: Partial<JobWindow>) =>
    onChange(windows.map((w, n) => (n === i ? { ...w, ...p } : w)));

  const toggleDay = (i: number, day: number) => {
    const days = windows[i].days ?? [];
    const next = days.includes(day) ? days.filter((d) => d !== day) : [...days, day].sort();
    set(i, { days: next });
  };

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-card-raised/40 p-2">
      {windows.length === 0 && (
        <p className="text-[11px] text-warn">
          No windows, so this kind never runs. Add one.
        </p>
      )}
      {windows.map((w, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <Input
            type="time"
            className="tnum h-7 w-24 font-mono text-[11px]"
            value={minutesToClock(w.startMinutes)}
            onChange={(e) => set(i, { startMinutes: clockToMinutes(e.target.value) })}
            aria-label={t("jobs.windowStart")}
          />
          <span className="text-[11px] text-muted-foreground">to</span>
          <Input
            type="time"
            className="tnum h-7 w-24 font-mono text-[11px]"
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
                  title={DAY_NAMES[day]}
                  aria-pressed={on}
                  onClick={() => toggleDay(i, day)}
                  className={cn(
                    "h-6 w-6 rounded text-[10px] font-semibold transition-colors",
                    on
                      ? "bg-primary-dim text-foreground"
                      : "text-muted-foreground hover:bg-accent",
                  )}
                >
                  {d}
                </button>
              );
            })}
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onChange(windows.filter((_, n) => n !== i))}
            aria-label={t("jobs.removeWindow")}
            className="hover:text-down"
          >
            <X />
          </Button>
          <span className="w-full text-[10px] text-muted-foreground">{windowSummary(w)}</span>
        </div>
      ))}
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
        Add window
      </Button>
    </div>
  );
}
