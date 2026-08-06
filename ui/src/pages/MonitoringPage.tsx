import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip as ReTooltip,
  XAxis,
  YAxis,
} from "recharts";
import { AlertTriangle, Eraser, Loader2, TerminalSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { StatusDot } from "@/components/signature/StatusDot";
import { useLiveData, useStaleTracker } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { bytes, clockTime, duration, kbps, pct } from "@/lib/format";
import { labelForState, toneBadge, toneForState } from "@/lib/signal";
import { cn } from "@/lib/utils";
import type {
  Destination,
  DryRunResult,
  ExpertResponse,
  LogLine,
  ProcessInfo,
} from "@/lib/types";
import { useT } from "@/lib/i18n";

const LOG_LEVEL_CLASS: Record<string, string> = {
  fatal: "text-down",
  error: "text-down",
  warning: "text-warn",
  info: "text-muted-foreground",
};

/** How much of the server's ring buffer to keep after merging every process.
 *  Deep enough to explain a restart that happened before the page opened,
 *  shallow enough that the tail stays a tail. */
const BACKFILL_LIMIT = 400;

const lineKey = (l: LogLine) => `${l.time} ${l.process} ${l.text}`;

/** Drops the historical lines the socket has already delivered.
 *
 *  The socket is open before the backfill request lands, so the tail of the
 *  ring buffer overlaps the head of the live feed. Each live line cancels one
 *  identical historical line rather than every match: FFmpeg repeats itself
 *  verbatim, and de-duplicating by key alone would silently thin the log. Both
 *  sides carry the same server-stamped time, so the keys line up exactly. */
function dedupe(history: LogLine[], live: LogLine[]): LogLine[] {
  if (history.length === 0 || live.length === 0) return history;
  const pending = new Map<string, number>();
  for (const l of live) {
    const k = lineKey(l);
    pending.set(k, (pending.get(k) ?? 0) + 1);
  }
  return history.filter((l) => {
    const k = lineKey(l);
    const n = pending.get(k) ?? 0;
    if (n === 0) return true;
    pending.set(k, n - 1);
    return false;
  });
}

/* ------------------------------------------------------------------ expert */

/** What is in the two boxes. Deliberately not ExpertArgs: the acknowledgement
 *  is its own piece of state, because ticking it must not invalidate the
 *  command the operator has already been shown. */
interface ExpertDraft {
  inputArgs: string;
  outputArgs: string;
}

/** Whether the resolved command on screen was built from the text currently in
 *  the boxes. Apply is gated on this: the whole point of the confirm step is
 *  that the argv the operator read is the argv that gets saved. */
function sameDraft(a: ExpertDraft | null, b: ExpertDraft): boolean {
  return a !== null && a.inputArgs === b.inputArgs && a.outputArgs === b.outputArgs;
}

const DRY_RUN_TONE: Record<DryRunResult["verdict"], string> = {
  ok: "text-live",
  invalid: "text-down",
  inconclusive: "text-warn",
};

const DRY_RUN_LABEL: Record<DryRunResult["verdict"], string> = {
  ok: "FFmpeg accepted it",
  invalid: "FFmpeg rejected an argument",
  inconclusive: "Nothing proven either way",
};

/** Expert mode: append hand-written FFmpeg arguments to one destination.
 *
 *  The flow is deliberately three steps rather than a text box and a save
 *  button. Type, resolve, then apply — and Apply does not light up until the
 *  full command shown below the boxes was built from the text currently in
 *  them. Someone pasting flags from a forum thread into a live stream should
 *  have to look at the whole line first. */
function ExpertPanel() {
  const t = useT();
  const [dests, setDests] = useState<Destination[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [draft, setDraft] = useState<ExpertDraft>({ inputArgs: "", outputArgs: "" });
  const [resolved, setResolved] = useState<ExpertResponse | null>(null);
  const [resolvedFor, setResolvedFor] = useState<ExpertDraft | null>(null);
  const [ack, setAck] = useState(false);
  const [dryRun, setDryRun] = useState<DryRunResult | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "dryrun" | "apply" | "clear">("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  useEffect(() => {
    api
      .listDestinations()
      .then((rows) => setDests(rows.map((r) => r.destination)))
      .catch(() => {});
  }, []);

  // Switching destination throws away everything about the previous one. A
  // resolved command carried across would describe the wrong process.
  const choose = useCallback((value: string) => {
    setSelected(value);
    setResolved(null);
    setResolvedFor(null);
    setDryRun(null);
    setError("");
    setSaved("");
    setDraft({ inputArgs: "", outputArgs: "" });
    setAck(false);
    void api.getExpert(Number(value))
      .then((r) => {
        setDraft({ inputArgs: r.args.inputArgs, outputArgs: r.args.outputArgs });
        setAck(r.args.ackReencode);
        setResolved(r);
        setResolvedFor({ inputArgs: r.args.inputArgs, outputArgs: r.args.outputArgs });
      })
      .catch((e: Error) => setError(e.message));
  }, []);

  const edit = useCallback((patch: Partial<ExpertDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    // The command on screen no longer describes what is in the boxes, so it
    // stops counting as something the operator was shown.
    setResolvedFor(null);
    setDryRun(null);
    setSaved("");
  }, []);

  const run = useCallback(
    async (
      kind: "preview" | "dryrun" | "apply" | "clear",
      fn: () => Promise<void>,
    ) => {
      setBusy(kind);
      setError("");
      try {
        await fn();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy("");
      }
    },
    [],
  );

  const preview = useCallback(
    () =>
      run("preview", async () => {
        const snapshot = draft;
        const r = await api.previewExpert(Number(selected), { ...snapshot, ackReencode: ack });
        setResolved(r);
        setResolvedFor(snapshot);
        setSaved("");
      }),
    [run, draft, selected, ack],
  );

  const doDryRun = useCallback(
    () =>
      run("dryrun", async () => {
        setDryRun(
          await api.dryRunExpert(Number(selected), { ...draft, ackReencode: ack }),
        );
      }),
    [run, draft, selected, ack],
  );

  const apply = useCallback(
    () =>
      run("apply", async () => {
        const r = await api.putExpert(Number(selected), { ...draft, ackReencode: ack, confirm: true });
        setResolved(r);
        setResolvedFor(draft);
        setSaved(r.warning ?? "Saved.");
      }),
    [run, draft, selected, ack],
  );

  const clear = useCallback(
    () =>
      run("clear", async () => {
        const r = await api.deleteExpert(Number(selected));
        setDraft({ inputArgs: "", outputArgs: "" });
        setAck(false);
        setDryRun(null);
        setResolved(r);
        setResolvedFor({ inputArgs: "", outputArgs: "" });
        setSaved("Cleared. This destination is back on the generated command.");
      }),
    [run, selected],
  );

  const guards = resolved?.guards ?? [];
  const shown = sameDraft(resolvedFor, draft);
  const blocked = guards.length > 0 && !ack;
  const canApply = selected !== "" && shown && !blocked && busy === "";

  return (
    <Card className="mt-3">
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle className="flex items-center gap-1.5">
          <TerminalSquare className="h-3.5 w-3.5" />
          Expert mode — extra FFmpeg arguments
        </CardTitle>
        <Select value={selected} onValueChange={choose}>
          <SelectTrigger className="h-7 w-56 text-[11px]">
            <SelectValue placeholder={t("mon.chooseDestination")} />
          </SelectTrigger>
          <SelectContent>
            {dests.map((d) => (
              <SelectItem key={d.id} value={String(d.id)}>
                {d.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </CardHeader>

      <CardContent className="flex flex-col gap-2.5">
        <p className="text-[11px] text-muted-foreground">
          These two strings are appended to the generated command — input arguments immediately
          before <code className="font-mono">-i</code>, output arguments immediately before the
          publish target. Nothing else is replaced, so the routing graph this destination was
          configured with still decides which audio tracks it receives. Read the resolved command
          below before applying.
        </p>

        {selected === "" ? (
          <div className="rounded border border-dashed border-border py-6 text-center text-[11px] text-muted-foreground">
            Choose a destination to edit its command line.
          </div>
        ) : (
          <>
            <div className="grid gap-2.5 sm:grid-cols-2">
              <div>
                <Label htmlFor="expert-in">{t("mon.extraInput")}</Label>
                <Textarea
                  id="expert-in"
                  spellCheck={false}
                  className="mt-1 font-mono"
                  placeholder="-thread_queue_size 2048"
                  value={draft.inputArgs}
                  onChange={(e) => edit({ inputArgs: e.target.value })}
                />
              </div>
              <div>
                <Label htmlFor="expert-out">{t("mon.extraOutput")}</Label>
                <Textarea
                  id="expert-out"
                  spellCheck={false}
                  className="mt-1 font-mono"
                  placeholder="-muxdelay 0.1"
                  value={draft.outputArgs}
                  onChange={(e) => edit({ outputArgs: e.target.value })}
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant="secondary" onClick={preview} disabled={busy !== ""}>
                {busy === "preview" && <Loader2 className="h-3 w-3 animate-spin" />}
                Resolve command
              </Button>
              <Button size="sm" variant="secondary" onClick={doDryRun} disabled={busy !== ""}>
                {busy === "dryrun" && <Loader2 className="h-3 w-3 animate-spin" />}
            {t("mon.dryRun")}
              </Button>
              <Button size="sm" onClick={apply} disabled={!canApply}>
                {busy === "apply" && <Loader2 className="h-3 w-3 animate-spin" />}
                Apply
              </Button>
              <Button size="sm" variant="ghost" onClick={clear} disabled={busy !== ""}>
                Clear
              </Button>
              {!shown && (
                <span className="text-[10px] text-muted-foreground">
            {t("mon.resolveHint")}
                </span>
              )}
            </div>

            {error && (
              <div className="flex gap-1.5 rounded border border-down/30 bg-down-dim p-2 text-[11px] text-down">
                <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
                <span className="break-words">{error}</span>
              </div>
            )}

            {guards.map((g) => (
              <div
                key={g.arg}
                className="flex gap-1.5 rounded border border-warn/30 bg-warn-dim p-2 text-[11px] text-warn"
              >
                <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
                <span className="break-words">{g.reason}</span>
              </div>
            ))}

            {guards.length > 0 && (
              <div className="flex items-center gap-2">
                <Checkbox
                  id="expert-ack"
                  checked={ack}
                  onCheckedChange={(v) => setAck(v === true)}
                />
                <Label htmlFor="expert-ack" className="cursor-pointer">
                  I understand this overrides what polyemesis otherwise guarantees for this
                  destination.
                </Label>
              </div>
            )}

            {resolved?.warning && !error && (
              <div className="rounded border border-warn/30 bg-warn-dim p-2 text-[11px] text-warn">
                {resolved.warning}
              </div>
            )}
            {saved && !error && (
              <div className="rounded border border-border bg-surface p-2 text-[11px] text-muted-foreground">
                {saved}
              </div>
            )}

            {resolved?.command?.command && (
              <div>
                <div className="mb-1 flex items-center gap-2">
                  <span className="text-[10px] uppercase tracking-wide text-subtle-foreground">
            {t("mon.fullCommand")}
                  </span>
                  <Badge variant={resolved.command.live ? "default" : "outline"}>
                    {resolved.command.live ? t("mon.fromRunningProcess") : t("mon.rebuilt")}
                  </Badge>
                  {!shown && <Badge variant="outline">stale</Badge>}
                </div>
                <pre
                  className={cn(
                    "max-h-56 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-background p-2 font-mono text-[10px]",
                    shown ? "text-foreground" : "text-subtle-foreground",
                  )}
                >
                  {resolved.command.command}
                </pre>
                {resolved.command.note && (
                  <p className="mt-1 text-[10px] text-muted-foreground">{resolved.command.note}</p>
                )}
              </div>
            )}

            {dryRun && (
              <div>
                <div className="mb-1 flex items-center gap-2 text-[11px]">
                  <span className="text-[10px] uppercase tracking-wide text-subtle-foreground">
            {t("mon.dryRun")}
                  </span>
                  <span className={DRY_RUN_TONE[dryRun.verdict]}>
                    {DRY_RUN_LABEL[dryRun.verdict]}
                  </span>
                </div>
                {dryRun.message && (
                  <p className="text-[11px] text-muted-foreground">{dryRun.message}</p>
                )}
                {dryRun.output && (
                  <details className="mt-1">
                    <summary className="cursor-pointer text-[10px] text-muted-foreground hover:text-foreground">
                      FFmpeg output
                    </summary>
                    <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-1.5 font-mono text-[9px] text-muted-foreground">
                      {dryRun.output}
                    </pre>
                  </details>
                )}
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

export function MonitoringPage() {
  const t = useT();
  const { system, bitrate, logs, status, clearLogs } = useLiveData();
  const [processes, setProcesses] = useState<ProcessInfo[]>([]);
  const [history, setHistory] = useState<LogLine[]>([]);
  const [filter, setFilter] = useState("all");
  const [follow, setFollow] = useState(true);
  const logRef = useRef<HTMLDivElement>(null);
  const logsRef = useRef(logs);

  useEffect(() => {
    logsRef.current = logs;
  }, [logs]);

  // Process list changes rarely; poll rather than adding another event type.
  //
  // The failure is tracked rather than swallowed. A single failed poll is not
  // worth a toast -- the next one is five seconds away -- but silence must not
  // be indefinite, or the table keeps showing processes that stopped existing
  // and reads as current.
  const procFreshness = useStaleTracker();
  useEffect(() => {
    const load = () =>
      api
        .listProcesses()
        .then((p) => {
          setProcesses(p);
          procFreshness.ok();
        })
        .catch(procFreshness.failed);
    load();
    const t = window.setInterval(load, 5000);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The socket only carries lines produced after it connected, so a page opened
  // mid-session would start blank. Drain each process's ring buffer once to
  // give the tail some past; the socket keeps appending from there. This runs
  // its own listProcesses rather than waiting on the poll above, so it stays a
  // genuine one-shot instead of re-firing on every poll result.
  useEffect(() => {
    let cancelled = false;
    void api
      .listProcesses()
      .then((procs) =>
        Promise.all(
          Array.from(new Set(procs.map((p) => p.status.name))).map((n) =>
            api
              .processLogs(n)
              .then((r) => r.lines ?? [])
              .catch(() => [] as LogLine[]),
          ),
        ),
      )
      .then((batches) => {
        if (cancelled) return;
        const ordered = batches
          .flat()
          .sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
        setHistory(dedupe(ordered.slice(-BACKFILL_LIMIT), logsRef.current));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const merged = useMemo(
    () => (history.length === 0 ? logs : [...history, ...logs]),
    [history, logs],
  );

  const filtered = useMemo(
    () => (filter === "all" ? merged : merged.filter((l) => l.process === filter)),
    [merged, filter],
  );

  // Clearing has to take the backfill with it, or the "cleared" log would
  // instantly redraw everything the ring buffer still holds.
  const clearAll = useCallback(() => {
    setHistory([]);
    clearLogs();
  }, [clearLogs]);

  // Tail behaviour: stick to the bottom while following, so a live log reads
  // like `tail -f` rather than a scroll position you have to chase.
  useEffect(() => {
    if (!follow || !logRef.current) return;
    logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [filtered, follow]);

  const chartData = useMemo(
    () =>
      bitrate.map((s) => ({
        t: new Date(s.t).getTime(),
        kbps: Math.round(s.kbps),
      })),
    [bitrate],
  );

  const processNames = useMemo(
    () => Array.from(new Set(processes.map((p) => p.status.name))).sort(),
    [processes],
  );

  return (
    <div className="p-3">
      <PageHeader
        title={t("mon.title")}
        subtitle={t("mon.subtitle")}
      />

      {/* ---------- host + relay ---------- */}
      <div className="mb-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Card>
          <CardContent className="pt-3">
            <Stat
              label={t("mon.hostCpu")}
              value={pct(system?.cpuPercent ?? 0)}
              tone={(system?.cpuPercent ?? 0) > 85 ? "warn" : "default"}
            />
            <div className="mt-1 text-[10px] text-muted-foreground">
              {system?.numCpu ?? 0} cores · polyemesis {pct(system?.procCpuPercent ?? 0)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              label={t("mon.memory")}
              value={pct(system?.memPercent ?? 0)}
              tone={(system?.memPercent ?? 0) > 90 ? "warn" : "default"}
            />
            <div className="mt-1 text-[10px] text-muted-foreground">
              {bytes(system?.memUsedBytes ?? 0)} / {bytes(system?.memTotalBytes ?? 0)} · rss{" "}
              {bytes(system?.procMemBytes ?? 0)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat label={t("mon.relayIn")} value={bytes(status?.relay.rxBytes ?? 0)} />
            <div className="mt-1 text-[10px] text-muted-foreground">
              {status?.relay.subscribers?.length ?? 0} subscribers · port {status?.relay.port ?? "—"}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              label={t("mon.relayDrops")}
              value={status?.relay.dropped ?? 0}
              tone={(status?.relay.dropped ?? 0) > 0 ? "warn" : "muted"}
            />
            <div className="mt-1 text-[10px] text-muted-foreground">
              sends to a consumer that had not bound its port yet
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              label={t("mon.ingestLoss")}
              value={`${(status?.relay.lossPercent ?? 0).toFixed(2)}%`}
              tone={(status?.relay.lossPercent ?? 0) > 0 ? "warn" : "muted"}
            />
            <div className="mt-1 text-[10px] text-muted-foreground">
              {(status?.relay.tsLost ?? 0).toLocaleString()} of{" "}
              {((status?.relay.tsPackets ?? 0) + (status?.relay.tsLost ?? 0)).toLocaleString()} TS
              packets missing · {(status?.relay.discontinuities ?? 0).toLocaleString()} breaks
            </div>
          </CardContent>
        </Card>
      </div>

      {/* ---------- ingest bitrate ---------- */}
      <Card className="mb-3">
        <CardHeader>
          <CardTitle>{t("mon.bitrateChart")}</CardTitle>
        </CardHeader>
        <CardContent className="h-40 px-1">
          {chartData.length < 2 ? (
            <div className="flex h-full items-center justify-center text-[11px] text-muted-foreground">
              Collecting samples…
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={chartData} margin={{ top: 4, right: 8, bottom: 0, left: -8 }}>
                <defs>
                  <linearGradient id="bitrateFill" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="var(--primary)" stopOpacity={0.35} />
                    <stop offset="100%" stopColor="var(--primary)" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid stroke="var(--border)" strokeDasharray="2 4" vertical={false} />
                <XAxis
                  dataKey="t"
                  type="number"
                  domain={["dataMin", "dataMax"]}
                  tickFormatter={(v) => new Date(v).toLocaleTimeString(undefined, { hour12: false }).slice(0, 5)}
                  stroke="var(--subtle-foreground)"
                  tick={{ fontSize: 10, fontFamily: "var(--font-mono)" }}
                  tickLine={false}
                  axisLine={false}
                  minTickGap={40}
                />
                <YAxis
                  stroke="var(--subtle-foreground)"
                  tick={{ fontSize: 10, fontFamily: "var(--font-mono)" }}
                  tickLine={false}
                  axisLine={false}
                  width={44}
                />
                <ReTooltip
                  contentStyle={{
                    background: "var(--popover)",
                    border: "1px solid var(--border-strong)",
                    borderRadius: 6,
                    fontSize: 11,
                    fontFamily: "var(--font-mono)",
                  }}
                  labelFormatter={(v) => new Date(v as number).toLocaleTimeString(undefined, { hour12: false })}
                  formatter={(v) => [kbps(v as number), "ingest"]}
                />
                <Area
                  type="monotone"
                  dataKey="kbps"
                  stroke="var(--primary)"
                  strokeWidth={1.5}
                  fill="url(#bitrateFill)"
                  isAnimationActive={false}
                  dot={false}
                />
              </AreaChart>
            </ResponsiveContainer>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-3 lg:grid-cols-[22rem_minmax(0,1fr)]">
        {/* ---------- processes ---------- */}
        <Card className="h-fit">
          <CardHeader>
            <CardTitle>{t("mon.processes")}</CardTitle>
              {procFreshness.stale && (
                <Badge variant="warn" title={`${procFreshness.failures} consecutive failed refreshes`}>
                  not updating
                </Badge>
              )}
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5">
            {processes.length === 0 && (
              <div className="py-3 text-center text-[11px] text-muted-foreground">
                <Loader2 className="mx-auto h-3.5 w-3.5 animate-spin" />
              </div>
            )}
            {processes.map(({ status: p, command }) => {
              const tone = toneForState(p.state);
              return (
                <div key={p.name} className="rounded-md border border-border bg-background p-2">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex min-w-0 items-center gap-1.5">
                      <StatusDot tone={tone} size="sm" />
                      <span className="truncate font-mono text-[11px]">{p.name}</span>
                    </div>
                    <Badge variant={toneBadge[tone]}>{labelForState(p.state)}</Badge>
                  </div>
                  <div className="mt-1 grid grid-cols-3 gap-1">
                    <Stat label={t("mon.pid")} value={p.pid || "—"} tone="muted" />
                    <Stat
                      label={t("mon.uptime")}
                      value={p.state === "running" ? duration(p.uptimeSec) : "—"}
                      tone="muted"
                    />
                    <Stat
                      label={t("mon.restarts")}
                      value={p.restarts}
                      tone={p.restarts > 0 ? "warn" : "muted"}
                    />
                  </div>
                  {p.nextRetryIn ? (
                    <div className="mt-1 text-[10px] text-warn">
                      retrying in {p.nextRetryIn.toFixed(0)}s
                    </div>
                  ) : null}
                  {p.lastError && (
                    <div className="mt-1 line-clamp-2 break-words text-[10px] text-down">
                      {p.lastError}
                    </div>
                  )}
                  <details className="mt-1">
                    <summary className="cursor-pointer text-[10px] text-muted-foreground hover:text-foreground">
                      command line
                    </summary>
                    <pre className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-1.5 font-mono text-[9px] text-muted-foreground">
                      {command}
                    </pre>
                  </details>
                </div>
              );
            })}
          </CardContent>
        </Card>

        {/* ---------- log tail ---------- */}
        <Card className="flex min-h-0 flex-col">
          <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
            <CardTitle>{t("mon.ffmpegLog")}</CardTitle>
            <div className="flex items-center gap-2">
              <Select value={filter} onValueChange={setFilter}>
                <SelectTrigger className="h-7 w-36 text-[11px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("mon.allProcesses")}</SelectItem>
                  {processNames.map((n) => (
                    <SelectItem key={n} value={n}>
                      {n}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <div className="flex items-center gap-1.5">
                <Switch id="follow" checked={follow} onCheckedChange={setFollow} />
                <Label htmlFor="follow" className="cursor-pointer">
                  Follow
                </Label>
              </div>
              <Button variant="ghost" size="icon-sm" onClick={clearAll} aria-label={t("mon.clearLog")}>
                <Eraser />
              </Button>
            </div>
          </CardHeader>
          <CardContent>
            <div
              ref={logRef}
              className="h-[26rem] overflow-y-auto rounded border border-border bg-background p-2 font-mono text-[10px] leading-relaxed"
            >
              {filtered.length === 0 ? (
                <div className="py-6 text-center text-muted-foreground">
                  No log lines yet. FFmpeg only writes when something is worth saying.
                </div>
              ) : (
                filtered.map((l, i) => (
                  <div key={i} className="flex gap-2">
                    <span className="tnum shrink-0 text-subtle-foreground">
                      {clockTime(l.time)}
                    </span>
                    <span className="shrink-0 text-primary/70">{l.process}</span>
                    <span className={cn("break-all", LOG_LEVEL_CLASS[l.level] ?? "")}>
                      {l.text}
                    </span>
                  </div>
                ))
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      <ExpertPanel />
    </div>
  );
}
