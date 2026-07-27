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
import { Eraser, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
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
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { bytes, clockTime, duration, kbps, pct } from "@/lib/format";
import { labelForState, toneBadge, toneForState } from "@/lib/signal";
import { cn } from "@/lib/utils";
import type { LogLine, ProcessInfo } from "@/lib/types";

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

export function MonitoringPage() {
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
  useEffect(() => {
    const load = () => api.listProcesses().then(setProcesses).catch(() => {});
    load();
    const t = window.setInterval(load, 5000);
    return () => window.clearInterval(t);
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
        title="Monitoring"
        subtitle="Process health, host resources, and the live FFmpeg log."
      />

      {/* ---------- host + relay ---------- */}
      <div className="mb-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Card>
          <CardContent className="pt-3">
            <Stat
              label="Host CPU"
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
              label="Memory"
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
            <Stat label="Relay in" value={bytes(status?.relay.rxBytes ?? 0)} />
            <div className="mt-1 text-[10px] text-muted-foreground">
              {status?.relay.subscribers?.length ?? 0} subscribers · port {status?.relay.port ?? "—"}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-3">
            <Stat
              label="Relay drops"
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
              label="Ingest loss"
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
          <CardTitle>Ingest bitrate — last 30 minutes</CardTitle>
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
            <CardTitle>Processes</CardTitle>
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
                    <Stat label="PID" value={p.pid || "—"} tone="muted" />
                    <Stat
                      label="Uptime"
                      value={p.state === "running" ? duration(p.uptimeSec) : "—"}
                      tone="muted"
                    />
                    <Stat
                      label="Restarts"
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
            <CardTitle>FFmpeg log</CardTitle>
            <div className="flex items-center gap-2">
              <Select value={filter} onValueChange={setFilter}>
                <SelectTrigger className="h-7 w-36 text-[11px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All processes</SelectItem>
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
              <Button variant="ghost" size="icon-sm" onClick={clearAll} aria-label="Clear log">
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
    </div>
  );
}
