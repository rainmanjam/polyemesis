import { InfoHint } from "@/components/InfoHint";
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
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { MonitorTimeline } from "@/components/MonitorTimeline";
import { Stat } from "@/components/signature/Stat";
import { FailureRemedy } from "@/components/signature/FailureRemedy";
import { Link, useSearchParams } from "react-router";
import { StatusDot } from "@/components/signature/StatusDot";
import { useLiveData, useStaleTracker } from "@/hooks/useLiveData";
import { useConfirm } from "@/hooks/useConfirm";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { api } from "@/lib/api";
import { bytes, clockTime, duration, kbps, pct } from "@/lib/format";
import { toneForState } from "@/lib/signal";
import { processStatus } from "@/lib/status";
import { StatusPill } from "@/components/StatusPill";
import { EmptyState } from "@/components/EmptyState";
import {
  VERDICT_WINDOW_SEC,
  collapseRepeats,
  countLevels,
  filterLines,
  isErrorLine,
  type LogLevelFilter,
  verdict,
} from "@/lib/logSummary";
import { cn } from "@/lib/utils";
import {
  asDestinationId,
  type Destination,
  type DryRunResult,
  type ExpertResponse,
  type LogLine,
  type ProcessInfo,
} from "@/lib/types";
import { useT, useTN, type TranslationKey } from "@/lib/i18n";

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

const DRY_RUN_LABEL: Record<DryRunResult["verdict"], TranslationKey> = {
  ok: "mon.dryRunOk",
  invalid: "mon.dryRunInvalid",
  inconclusive: "mon.nothingProvenEitherWay",
};

/** Expert mode: append hand-written FFmpeg arguments to one destination.
 *
 *  The flow is deliberately three steps rather than a text box and a save
 *  button. Type, resolve, then apply — and Apply does not light up until the
 *  full command shown below the boxes was built from the text currently in
 *  them. Someone pasting flags from a forum thread into a live stream should
 *  have to look at the whole line first. */
export function ExpertPanel() {
  const t = useT();
  const tn = useTN();
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
  // Clear is a WRITE THAT RESTARTS A LIVE OUTPUT: the server saves empty args
  // and reconciles, the spec hash changes, and the destination's ffmpeg is
  // replaced. It used to fire on one click of a ghost button beside Apply --
  // which itself takes resolve-then-apply to reach. The only path to `clear`
  // now runs through this confirmation, which names the destination.
  const confirmClear = useConfirm<Destination>();

  // A failed list is said, with a retry, not left as an empty picker that reads
  // as "there are no destinations" (A70).
  const [destsFailed, setDestsFailed] = useState(false);
  const loadDests = useCallback(() => {
    api
      .listDestinations()
      .then((rows) => {
        setDests(rows.map((r) => r.destination));
        setDestsFailed(false);
      })
      .catch(() => setDestsFailed(true));
  }, []);
  useEffect(() => {
    loadDests();
  }, [loadDests]);

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
    void api.getExpert(asDestinationId(Number(value)))
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
        const r = await api.previewExpert(asDestinationId(Number(selected)), { ...snapshot, ackReencode: ack });
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
          await api.dryRunExpert(asDestinationId(Number(selected)), { ...draft, ackReencode: ack }),
        );
      }),
    [run, draft, selected, ack],
  );

  const apply = useCallback(
    () =>
      run("apply", async () => {
        const r = await api.putExpert(asDestinationId(Number(selected)), { ...draft, ackReencode: ack, confirm: true });
        setResolved(r);
        setResolvedFor(draft);
        setSaved(r.warning ?? "Saved.");
      }),
    [run, draft, selected, ack],
  );

  const clear = useCallback(
    (dest: Destination) =>
      run("clear", async () => {
        const r = await api.deleteExpert(asDestinationId(dest.id));
        setDraft({ inputArgs: "", outputArgs: "" });
        setAck(false);
        setDryRun(null);
        setResolved(r);
        setResolvedFor({ inputArgs: "", outputArgs: "" });
        setSaved(t("mon.clearedThisDestinationIsBack"));
      }),
    [run, t],
  );
  const selectedDest = dests.find((d) => String(d.id) === selected);

  const guards = resolved?.guards ?? [];
  const shown = sameDraft(resolvedFor, draft);
  const blocked = guards.length > 0 && !ack;
  const canApply = selected !== "" && shown && !blocked && busy === "";

  return (
    <Card className="mt-3">
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle className="flex items-center gap-1.5">
          <TerminalSquare className="h-3.5 w-3.5" />
          {t("i18nSweep.expertModeTitle")}
        </CardTitle>
        <Select value={selected} onValueChange={choose}>
          <SelectTrigger className="h-7 w-56 text-tiny">
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
        {destsFailed && (
          <div role="alert" className="flex flex-wrap items-center gap-2 text-tiny text-down">
            <span>{t("mon.destinationsLoadFailed")}</span>
            <Button size="sm" variant="outline" onClick={loadDests}>
              {t("common.retry")}
            </Button>
          </div>
        )}
        <p className="text-tiny text-muted-foreground">
          {tn("mon.expertIntro", { flag: <code className="font-mono">-i</code> })}
        </p>

        {selected === "" ? (
          <EmptyState title={t("mon.chooseDestinationToEdit")} />
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
                {t("i18nSweep.resolveCommand")}
              </Button>
              <Button size="sm" variant="secondary" onClick={doDryRun} disabled={busy !== ""}>
                {busy === "dryrun" && <Loader2 className="h-3 w-3 animate-spin" />}
            {t("mon.dryRun")}
              </Button>
              <Button size="sm" onClick={apply} disabled={!canApply}>
                {busy === "apply" && <Loader2 className="h-3 w-3 animate-spin" />}
                {t("common.apply")}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => selectedDest && confirmClear.ask(selectedDest)}
                disabled={busy !== "" || !selectedDest}
              >
                {t("mon.clear")}
              </Button>
              {!shown && (
                <span className="text-sm text-muted-foreground">
            {t("mon.resolveHint")}
                </span>
              )}
            </div>

            {error && (
              <div className="flex gap-1.5 rounded border border-down/30 bg-down-dim p-2 text-tiny text-down">
                <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
                <span className="break-words">{error}</span>
              </div>
            )}

            {guards.map((g) => (
              <div
                key={g.arg}
                className="flex gap-1.5 rounded border border-warn/30 bg-warn-dim p-2 text-tiny text-warn"
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
                  {t("mon.expertAck")}
                </Label>
              </div>
            )}

            {resolved?.warning && !error && (
              <div className="rounded border border-warn/30 bg-warn-dim p-2 text-tiny text-warn">
                {resolved.warning}
              </div>
            )}
            {saved && !error && (
              <div className="rounded border border-border bg-surface p-2 text-tiny text-muted-foreground">
                {saved}
              </div>
            )}

            {resolved?.command?.command && (
              <div>
                <div className="mb-1 flex items-center gap-2">
                  <span className="text-micro uppercase tracking-wide text-muted-foreground">
            {t("mon.fullCommand")}
                  </span>
                  <Badge variant={resolved.command.live ? "default" : "outline"}>
                    {resolved.command.live ? t("mon.fromRunningProcess") : t("mon.rebuilt")}
                  </Badge>
                  {!shown && <Badge variant="outline">{t("i18nSweep.stale")}</Badge>}
                </div>
                <pre
                  className={cn(
                    "max-h-56 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-background p-2 font-mono text-micro",
                    shown ? "text-foreground" : "text-muted-foreground",
                  )}
                >
                  {resolved.command.command}
                </pre>
                {resolved.command.note && (
                  <p className="mt-1 text-sm text-muted-foreground">{resolved.command.note}</p>
                )}
              </div>
            )}

            {dryRun && (
              <div>
                <div className="mb-1 flex items-center gap-2 text-tiny">
                  <span className="text-micro uppercase tracking-wide text-muted-foreground">
            {t("mon.dryRun")}
                  </span>
                  <span className={DRY_RUN_TONE[dryRun.verdict]}>
                    {t(DRY_RUN_LABEL[dryRun.verdict])}
                  </span>
                </div>
                {dryRun.message && (
                  <p className="text-tiny text-muted-foreground">{dryRun.message}</p>
                )}
                {dryRun.output && (
                  <details className="mt-1">
                    <summary className="cursor-pointer text-sm text-muted-foreground hover:text-foreground">
                      {t("i18nSweep.ffmpegOutput")}
                    </summary>
                    <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-1.5 font-mono text-micro text-muted-foreground">
                      {dryRun.output}
                    </pre>
                  </details>
                )}
              </div>
            )}
          </>
        )}
      </CardContent>
      {/* Recoverable -- the arguments can be typed back -- so a button, not a
          typed challenge. The friction is naming the destination and saying
          that a live output restarts, which is the part a click on "Clear"
          did not tell anybody. */}
      <ConfirmDestructive
        open={confirmClear.open}
        onOpenChange={confirmClear.onOpenChange}
        subject={confirmClear.target?.name ?? ""}
        title={t("mon.clearExpertTitle")}
        description={t("mon.clearExpertBody")}
        confirmLabel={t("mon.clearExpertConfirm")}
        onConfirm={async () => {
          if (confirmClear.target) await clear(confirmClear.target);
          confirmClear.close();
        }}
      />
    </Card>
  );
}

/** A9 on a stat tile: shown only while the tile is in its warn tone, so a
 *  healthy page carries no advice nobody needs. One sentence and one control,
 *  a link to where it is fixed or a jump to the log. */
function TileRemedy({
  text,
  label,
  to,
  onClick,
}: {
  text: string;
  label: string;
  to?: string;
  onClick?: () => void;
}) {
  return (
    <div data-testid="tile-remedy" className="mt-1.5 flex flex-col items-start gap-1 text-sm text-warn">
      <span>{text}</span>
      {to ? (
        <Link to={to} className="underline underline-offset-2 hover:text-foreground">
          {label}
        </Link>
      ) : (
        <button type="button" onClick={onClick} className="underline underline-offset-2 hover:text-foreground">
          {label}
        </button>
      )}
    </div>
  );
}

export function MonitoringPage() {
  const t = useT();
  const {
    system, bitrate, logs, status, clearLogs, programme, programmeKnown, programmes, selectProgramme,
  } = useLiveData();
  const [processes, setProcesses] = useState<ProcessInfo[]>([]);
  // False until the first answer. Before it, an empty list is loading; after it,
  // an empty list is the truth: nothing is running (A68).
  const [processesLoaded, setProcessesLoaded] = useState(false);
  // Expert mode (raw FFmpeg arguments) is closed until asked for (A67).
  const [expertOpen, setExpertOpen] = useState(false);
  // The CPU warning is the Jobs CPU ceiling the operator saved, so the two pages
  // cannot disagree about when the machine is too busy (A71). 85 until read.
  const [cpuWarn, setCpuWarn] = useState(85);
  useEffect(() => {
    api
      .jobsOverview()
      .then((v) => setCpuWarn(v.policy.cpuCeilingPercent))
      .catch(() => setCpuWarn(85));
  }, []);
  const [history, setHistory] = useState<LogLine[]>([]);
  // ?process=<name> opens the log on one process: the "Show log" links on a
  // destination card and on a timeline row land here (A9, A13). Followed on
  // every change of the parameter, not only on mount, so a second link
  // clicked while the page is open still re-filters -- adjusted during
  // render, React's pattern for state that follows a prop.
  const [searchParams] = useSearchParams();
  const linked = searchParams.get("process");
  const [filter, setFilter] = useState(linked ?? "all");
  const [linkedSeen, setLinkedSeen] = useState(linked);
  if (linked !== linkedSeen) {
    setLinkedSeen(linked);
    if (linked) setFilter(linked);
  }
  // ?programme=<id> switches to that programme first (A13). Process names are
  // per programme -- every programme's ingest is "ingest" -- so a timeline row
  // about another programme's ingest needs this to open the right log rather
  // than this programme's log under the same name.
  //
  // APPLIED ONCE PER LINK, not held. The parameter stays in the URL after the
  // switch, and an operator who then picks another programme in the switcher
  // must not be dragged back by it. Waits for programmeKnown: before the list
  // is read, selectProgramme ignores an id it has not seen and the link would
  // be lost. An id the server does not list is ignored the same way.
  const linkKey = searchParams.toString();
  const appliedLink = useRef<string | null>(null);
  useEffect(() => {
    if (!programmeKnown || appliedLink.current === linkKey) return;
    appliedLink.current = linkKey;
    const raw = searchParams.get("programme");
    if (!raw || !/^\d+$/.test(raw)) return;
    const wanted = Number(raw);
    if (wanted !== programme && programmes.some((p) => p.id === wanted)) {
      selectProgramme(wanted);
    }
  }, [linkKey, searchParams, programmeKnown, programme, programmes, selectProgramme]);
  const logCardRef = useRef<HTMLDivElement>(null);
  // Lands on the log, filtered to one process. Used by the in-page "Show log"
  // controls; a link from another page arrives with #process-log instead.
  const showLog = useCallback((process: string) => {
    setFilter(process);
    logCardRef.current?.scrollIntoView({ block: "start" });
  }, []);
  // Keyed on the whole query, not on `linked`: two timeline rows about two
  // programmes' ingests carry the same process name, so the second click
  // changes only ?programme= and must still land on the log.
  useEffect(() => {
    if (linked && window.location.hash === "#process-log") {
      logCardRef.current?.scrollIntoView({ block: "start" });
    }
  }, [linked, linkKey]);
  const [follow, setFollow] = useState(true);
  // Errors is the default view: a healthy stream writes little, so the lines
  // worth reading first are the failures.
  const [level, setLevel] = useState<LogLevelFilter>("errors");
  const [search, setSearch] = useState("");
  const [showRaw, setShowRaw] = useState(false);
  // Ticks so the verdict's window slides forward while the log is quiet.
  const [nowMs, setNowMs] = useState(() => Date.now());
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
  // Destructured for the dependency list below; see MetersPage for why the
  // object itself must not be a dependency.
  const { ok: procOk, failed: procFailed } = procFreshness;
  useEffect(() => {
    if (!programmeKnown) return;
    const load = () =>
      api
        .listProcesses(programme)
        .then((p) => {
          setProcesses(p);
          setProcessesLoaded(true);
          procOk();
        })
        .catch(procFailed);
    load();
    const t = window.setInterval(load, 5000);
    return () => window.clearInterval(t);
    // programme, because listProcesses is scoped. An empty array froze it at
    // the mount value -- null -- so every poll went out unscoped and this whole
    // page was dead on any install with two programmes. Gated on
    // programmeKnown rather than on `programme != null`: null means either "no
    // sources", which the route accepts, or "not resolved yet", which it
    // refuses, and only the second is a bug.
  }, [programme, programmeKnown, procOk, procFailed]);

  // The socket only carries lines produced after it connected, so a page opened
  // mid-session would start blank. Drain each process's ring buffer once to
  // give the tail some past; the socket keeps appending from there. This runs
  // its own listProcesses rather than waiting on the poll above, so it stays a
  // genuine one-shot instead of re-firing on every poll result.
  useEffect(() => {
    if (!programmeKnown) return;
    let cancelled = false;
    void api
      .listProcesses(programme)
      .then((procs) =>
        Promise.all(
          Array.from(new Set(procs.map((p) => p.status.name))).map((n) =>
            api
              .processLogs(n, programme)
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
    // programme, for the reason the poll above gives. This is a one-shot
    // drain, so a frozen null meant the backlog was fetched unscoped once
    // and the panel opened empty with nothing saying why.
  }, [programme, programmeKnown]);

  const merged = useMemo(
    () => (history.length === 0 ? logs : [...history, ...logs]),
    [history, logs],
  );

  const filtered = useMemo(
    () => (filter === "all" ? merged : merged.filter((l) => l.process === filter)),
    [merged, filter],
  );

  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 5000);
    return () => window.clearInterval(id);
  }, []);

  const levelCounts = useMemo(() => countLevels(filtered), [filtered]);
  const shown = useMemo(
    () => filterLines(filtered, level, search),
    [filtered, level, search],
  );
  const groups = useMemo(() => collapseRepeats(shown), [shown]);
  const verdictState = useMemo(() => verdict(merged, nowMs), [merged, nowMs]);
  const errorsByProcess = useMemo(() => {
    const m = new Map<string, number>();
    for (const l of merged) {
      if (isErrorLine(l)) m.set(l.process, (m.get(l.process) ?? 0) + 1);
    }
    return m;
  }, [merged]);

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
  }, [groups, follow]);

  const chartData = useMemo(
    () =>
      bitrate.map((s) => ({
        t: new Date(s.t).getTime(),
        kbps: Math.round(s.kbps),
      })),
    [bitrate],
  );

  // localeCompare, not the default sort. Array.prototype.sort compares UTF-16
  // code units, which puts every capital before every lowercase: a filter list
  // holding "Recorder" and "dest:studio-a" orders the capital first regardless
  // of the alphabet, and an operator scanning for a name does not find it where
  // they look.
  const processNames = useMemo(
    () =>
      Array.from(new Set(processes.map((p) => p.status.name))).sort((a, b) =>
        a.localeCompare(b),
      ),
    [processes],
  );

  // THE ONE SENTENCE UNDER THE TITLE (A66). It says whether the stream is
  // healthy before any tile is read: failures first, then ingest loss, then
  // the calm case. It does not reuse the Dashboard verdict, which is still to
  // be unified (A3, A73); it reads the same process states the table does.
  const failedCount = processes.filter((p) => p.status.state === "failed").length;
  const runningCount = processes.filter((p) => p.status.state === "running").length;
  const losing = (status?.relay.dropped ?? 0) > 0 || (status?.relay.lossPercent ?? 0) > 0;

  return (
    <div className="p-3">
      <PageHeader
        title={t("mon.title")}
        subtitle={t("mon.subtitle")}
      />

      {processesLoaded && processes.length > 0 && (
        <p aria-live="polite" className={cn("mb-3 flex flex-wrap items-center gap-2 text-sm", failedCount > 0 ? "text-down" : "text-muted-foreground")}>
          <span>
            {failedCount > 0
              ? t("mon.verdictProcessesFailed", { count: failedCount })
              : losing
                ? t("mon.verdictIngestLoss")
                : runningCount === processes.length
                  ? t("mon.verdictAllRunning", { count: processes.length })
                  : t("mon.verdictSomeNotRunning", { running: runningCount, count: processes.length })}
          </span>
          {failedCount > 0 && (
            <Button
              variant="link"
              size="sm"
              className="h-auto p-0"
              onClick={() => logCardRef.current?.scrollIntoView({ block: "start" })}
            >
              {t("mon.verdictSeeLog")}
            </Button>
          )}
        </p>
      )}

      {/* ---------- host + relay ---------- */}
      <div className="mb-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Card>
          <CardContent className="pt-3">
            <Stat
              labelKey="mon.hostCpu"
              value={system ? pct(system.cpuPercent) : "—"}
              tone={(system?.cpuPercent ?? 0) > cpuWarn ? "warn" : "default"}
            />
            <div className="mt-1 text-sm text-muted-foreground">
              {t("i18nSweep.monCores", { cores: system?.numCpu ?? 0, pct: pct(system?.procCpuPercent ?? 0) })}
              <InfoHint body="mon.hostCpu.hint" title="mon.hostCpu" className="ml-1" />
            </div>
            <div className="mt-1 text-tiny text-muted-foreground">
              {t("mon.cpuWarnsAbove", { pct: cpuWarn })}
            </div>
            {(system?.cpuPercent ?? 0) > cpuWarn && (
              <TileRemedy text={t("mon.remedy.cpu")} to="/renditions" label={t("reason.action.renditions")} />
            )}
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              labelKey="mon.memory"
              value={system ? pct(system.memPercent) : "—"}
              tone={(system?.memPercent ?? 0) > 90 ? "warn" : "default"}
            />
            <div className="mt-1 text-sm text-muted-foreground">
              {/* app-council A93: the words around the figures are translated;
                  the bare "rss" was English in every locale. */}
              {t("mon.memProcess", {
                used: bytes(system?.memUsedBytes ?? 0),
                total: bytes(system?.memTotalBytes ?? 0),
                rss: bytes(system?.procMemBytes ?? 0),
              })}
              <InfoHint body="mon.memory.hint" title="mon.memory" className="ml-1" />
            </div>
            {(system?.memPercent ?? 0) > 90 && (
              <TileRemedy text={t("mon.remedy.memory")} to="/renditions" label={t("reason.action.renditions")} />
            )}
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat labelKey="mon.relayIn" value={status ? bytes(status.relay.rxBytes) : "—"} />
            <div className="mt-1 text-sm text-muted-foreground">
              {t("i18nSweep.monSubscribers", {
                count: status?.relay.subscribers?.length ?? 0,
                port: status?.relay.port ?? "—",
              })}
              <InfoHint body="mon.relayIn.hint" title="mon.relayIn" className="ml-1" />
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              labelKey="mon.relayDrops"
              value={status ? status.relay.dropped : "—"}
              tone={(status?.relay.dropped ?? 0) > 0 ? "warn" : "muted"}
            />
            <div className="mt-1 text-sm text-muted-foreground">
              {t("mon.relayDropsNote")}
              <InfoHint body="mon.relayDrops.hint" title="mon.relayDrops" className="ml-1" />
            </div>
            {(status?.relay.dropped ?? 0) > 0 && (
              <TileRemedy
                text={t("mon.remedy.relayDrops")}
                onClick={() => logCardRef.current?.scrollIntoView({ block: "start" })}
                label={t("reason.action.log")}
              />
            )}
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              labelKey="mon.ingestLoss"
              value={status ? `${status.relay.lossPercent.toFixed(2)}%` : "—"}
              tone={(status?.relay.lossPercent ?? 0) > 0 ? "warn" : "muted"}
            />
            <div className="mt-1 text-sm text-muted-foreground">
              {t("i18nSweep.monTsMissing", {
                lost: (status?.relay.tsLost ?? 0).toLocaleString(),
                total: ((status?.relay.tsPackets ?? 0) + (status?.relay.tsLost ?? 0)).toLocaleString(),
                breaks: (status?.relay.discontinuities ?? 0).toLocaleString(),
              })}
              <InfoHint body="mon.ingestLoss.hint" title="mon.ingestLoss" className="ml-1" />
            </div>
            {(status?.relay.lossPercent ?? 0) > 0 && (
              <TileRemedy text={t("mon.remedy.ingestLoss")} to="/sources" label={t("mon.action.sources")} />
            )}
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
            <div className="flex h-full items-center justify-center text-tiny text-muted-foreground">
              {t("i18nSweep.collectingSamples")}
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
                    // The text-sm step (12px), not an off-scale 11.
                    fontSize: 12,
                    fontFamily: "var(--font-mono)",
                  }}
                  labelFormatter={(v) => new Date(v as number).toLocaleTimeString(undefined, { hour12: false })}
                  formatter={(v) => [kbps(v as number), t("chrome.ingest")]}
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
        <div className="mb-3">
        <MonitorTimeline />
      </div>

      {/* ---------- processes ---------- */}
        <Card className="h-fit">
          <CardHeader>
            <CardTitle>{t("mon.processes")}</CardTitle>
              {procFreshness.stale && (
                <Badge variant="warn" title={t("i18nSweep.failedRefreshes", { count: procFreshness.failures })}>
                  {t("meters.notUpdating")}
                </Badge>
              )}
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5">
            {/* Loading is a spinner; an answered empty list is the kit's empty
                state, which says what the absence means (A68). */}
            {processes.length === 0 &&
              (processesLoaded ? (
                <EmptyState title={t("mon.noProcesses")} className="py-4" />
              ) : (
                <div className="py-3 text-center text-tiny text-muted-foreground">
                  <Loader2 className="mx-auto h-3.5 w-3.5 animate-spin" />
                </div>
              ))}
            {processes.map(({ status: p, command }) => {
              const tone = toneForState(p.state);
              return (
                <div key={p.name} className="rounded-md border border-border bg-background p-2">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex min-w-0 items-center gap-1.5">
                      <StatusDot tone={tone} size="sm" />
                      <span className="truncate font-mono text-tiny">{p.name}</span>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      {(errorsByProcess.get(p.name) ?? 0) > 0 && (
                        <Badge variant="warn">
                          {t("monitor.processErrors", { count: errorsByProcess.get(p.name) ?? 0 })}
                        </Badge>
                      )}
                      <StatusPill reading={processStatus(p.state)} />
                    </div>
                  </div>
                  <div className="mt-1 grid grid-cols-3 gap-1">
                    <Stat labelKey="mon.pid" value={p.pid || "—"} tone="muted" />
                    <Stat
                      labelKey="mon.uptime"
                      value={p.state === "running" ? duration(p.uptimeSec) : "—"}
                      tone="muted"
                    />
                    <Stat
                      labelKey="mon.restarts"
                      value={p.restarts}
                      tone={p.restarts > 0 ? "warn" : "muted"}
                    />
                  </div>
                  {p.nextRetryIn ? (
                    <div className="mt-1 text-sm text-warn">
                      {t("i18nSweep.retryingIn", { seconds: p.nextRetryIn.toFixed(0) })}
                    </div>
                  ) : null}
                  {/* #20 + A9: the classified cause, what to do about it, and the
                      process's own log one click away. The raw lastError stays
                      for an unclassified failure. */}
                  {p.reason ? (
                    <div className="mt-1 flex flex-col gap-1 text-sm text-down">
                      <span className="font-medium">{t(`reason.cause.${p.reason.cause}`)}</span>
                      <FailureRemedy
                        cause={p.reason.cause}
                        process={p.name}
                        onShowLog={() => showLog(p.name)}
                      />
                    </div>
                  ) : (
                    p.lastError && (
                      <div className="mt-1 line-clamp-2 break-words text-sm text-down">
                        {p.lastError}
                      </div>
                    )
                  )}
                  <details className="mt-1">
                    <summary className="cursor-pointer text-sm text-muted-foreground hover:text-foreground">
                      {t("i18nSweep.commandLine")}
                    </summary>
                    <pre className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-1.5 font-mono text-micro text-muted-foreground">
                      {command}
                    </pre>
                  </details>
                </div>
              );
            })}
          </CardContent>
        </Card>

      </div>

      {/* ---------- log tail ----------
          Full width, beneath Timeline and Processes. It sat in the 22rem column
          before, about 290px wide, which is where an operator least wants to
          read a decode error (A65). */}
      <Card id="process-log" ref={logCardRef} className="flex min-h-0 flex-col scroll-mt-3">
          <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <CardTitle>{t("mon.ffmpegLog")}</CardTitle>
              <Badge variant="down">{t("monitor.errorsCount", { count: levelCounts.errors })}</Badge>
              <Badge variant="warn">
                {t("monitor.warningsCount", { count: levelCounts.warnings })}
              </Badge>
            </div>
            <div className="flex items-center gap-2">
              <Select value={filter} onValueChange={setFilter}>
                <SelectTrigger className="h-7 w-36 text-tiny">
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
                  {t("i18nSweep.follow")}
                </Label>
              </div>
              <Button variant="ghost" size="icon-sm" onClick={clearAll} aria-label={t("mon.clearLog")}>
                <Eraser />
              </Button>
            </div>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <p role="status" className="text-tiny text-muted-foreground">
              {verdictState.kind === "clean"
                ? t("monitor.verdictClean", { window: VERDICT_WINDOW_SEC })
                : t("monitor.verdictErrors", {
                    process: verdictState.process,
                    count: verdictState.count,
                    window: verdictState.windowSec,
                  })}
            </p>
            {/* A9: the verdict names a process, so it offers that process's
                log -- outside the status line, which is read as one sentence. */}
            {verdictState.kind === "errors" && filter !== verdictState.process && (
              <div>
                <Button size="sm" variant="outline" onClick={() => showLog(verdictState.process)}>
                  {t("monitor.showProcessLog", { process: verdictState.process })}
                </Button>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <div className="flex gap-1">
                {(
                  [
                    ["errors", "monitor.filterErrors"],
                    ["warnings", "monitor.filterWarnings"],
                    ["all", "monitor.filterAll"],
                  ] as const
                ).map(([value, key]) => (
                  <Button
                    key={value}
                    variant={level === value ? "secondary" : "ghost"}
                    size="sm"
                    aria-pressed={level === value}
                    onClick={() => setLevel(value)}
                  >
                    {t(key)}
                  </Button>
                ))}
              </div>
              <Input
                type="search"
                className="h-7 max-w-xs flex-1 text-tiny"
                aria-label={t("monitor.searchLog")}
                placeholder={t("monitor.searchLog")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <div
              ref={logRef}
              className="h-[22rem] overflow-y-auto rounded border border-border bg-background p-2 font-mono text-micro leading-relaxed"
            >
              {/* app-council A83: the kit's empty state, set in the sans face
                  and without its own border inside the log box. */}
              {merged.length === 0 ? (
                <EmptyState
                  title={t("mon.noLogLinesTitle")}
                  body={t("mon.noLogLinesBody")}
                  className="border-0 font-sans"
                />
              ) : groups.length === 0 ? (
                <EmptyState title={t("monitor.noMatchingLines")} className="border-0 font-sans" />
              ) : (
                groups.map((g, i) => (
                  <div key={i} className="flex gap-2">
                    <span className="tnum shrink-0 text-muted-foreground">
                      {clockTime(g.line.time)}
                    </span>
                    <span className="shrink-0 text-primary/70">{g.line.process}</span>
                    {g.count > 1 && (
                      <span
                        className="shrink-0 rounded bg-muted px-1 tnum text-muted-foreground"
                        title={t("monitor.repeatTitle", { count: g.count })}
                      >
                        {`×${g.count}`}
                      </span>
                    )}
                    <span className={cn("break-all", LOG_LEVEL_CLASS[g.line.level] ?? "")}>
                      {g.line.text}
                    </span>
                  </div>
                ))
              )}
            </div>
            <div>
              <Button
                variant="ghost"
                size="sm"
                aria-expanded={showRaw}
                onClick={() => setShowRaw((v) => !v)}
              >
                {showRaw ? t("monitor.hideRaw") : t("monitor.showRaw")}
              </Button>
              {showRaw && (
                <div className="mt-2 h-48 overflow-y-auto rounded border border-border bg-background p-2 font-mono text-micro leading-relaxed">
                  {filtered.map((l, i) => (
                    <div key={i} className="flex gap-2">
                      <span className="tnum shrink-0 text-muted-foreground">
                        {clockTime(l.time)}
                      </span>
                      <span className="shrink-0 text-primary/70">{l.process}</span>
                      <span className={cn("break-all", LOG_LEVEL_CLASS[l.level] ?? "")}>
                        {l.text}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </CardContent>
      </Card>

      {/* Raw FFmpeg arguments are a power tool, so they are closed by default and
          do not mount until asked for. A live output's command is one deliberate
          click away, not a card on every visit (A67). */}
      <div className="mt-3">
        <Button
          variant="outline"
          size="sm"
          aria-expanded={expertOpen}
          onClick={() => setExpertOpen((v) => !v)}
        >
          {expertOpen ? t("mon.expertHide") : t("mon.expertShow")}
        </Button>
        {!expertOpen && (
          <p className="mt-1 text-tiny text-muted-foreground">{t("mon.expertClosedHint")}</p>
        )}
      </div>
      {expertOpen && <ExpertPanel />}
    </div>
  );
}
