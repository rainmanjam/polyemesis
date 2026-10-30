import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { BarChart3, Download, Loader2, Plus, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { accentFor } from "@/lib/chat";
import { useT, type Translator } from "@/lib/i18n";
import {
  POLL_LIMITS,
  clock,
  exportHref,
  isLive,
  platformsByVotes,
  pollProblem,
  secondsLeft,
  sharePercents,
  type Poll,
  type PollsResponse,
} from "@/lib/polls";
import type { ChatPlatform } from "@/lib/types";

/* ===========================================================================
   Chat polls (#14): start one, watch it count, end it, and find old ones.

   The panel shows what the operator needs and the on-stream widget does not:
   the per-platform breakdown, whether the announcement reached each chat, and
   whether the native Twitch poll was created and why not. It never shows a
   voter -- the server sends counts and nothing else.

   Live updates by polling the REST route every 1.5 s while a poll runs. A poll
   is short and the route is cheap; a second socket subscription for one panel
   would cost more than it saves.
   =========================================================================== */

const LIVE_EVERY_MS = 1500;
const HISTORY = 10;

function platformLabel(p: string): string {
  return accentFor(p as ChatPlatform).label || p;
}

export function PollPanel() {
  const t = useT();
  const [data, setData] = useState<PollsResponse | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const load = useCallback(async () => {
    try {
      setData(await api.listPolls(HISTORY));
      setUnavailable(false);
    } catch {
      setUnavailable(true);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const live = isLive(data?.current);
  useEffect(() => {
    if (!live) return;
    const id = window.setInterval(() => {
      setNow(Date.now());
      void load();
    }, LIVE_EVERY_MS);
    return () => window.clearInterval(id);
  }, [live, load]);

  const finished = (data?.history ?? []).filter((p) => p.id !== data?.current?.id);

  return (
    <section className="flex flex-col gap-2 rounded-md border border-border bg-card p-2" aria-label={t("polls.title")}>
      <div className="flex items-center gap-2">
        <BarChart3 className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-[12px] font-semibold">{t("polls.title")}</span>
      </div>

      {unavailable && <p className="text-sm text-muted-foreground">{t("polls.unavailable")}</p>}

      {!unavailable && data?.current && live && <LivePoll poll={data.current} now={now} onChanged={load} />}
      {!unavailable && !live && <StartForm onStarted={load} />}

      {finished.length > 0 && (
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm font-semibold">{t("polls.history")}</span>
            <a className="inline-flex items-center gap-1 text-sm text-primary underline-offset-2 hover:underline" href={exportHref()} download>
              <Download className="h-3 w-3" /> {t("polls.exportAll")}
            </a>
          </div>
          <ul className="flex flex-col gap-1">
            {finished.map((p) => (
              <HistoryRow key={p.id} poll={p} />
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function StartForm({ onStarted }: { onStarted: () => Promise<void> }) {
  const t = useT();
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState<string[]>(["", ""]);
  const [duration, setDuration] = useState(120);
  const [mirror, setMirror] = useState(true);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const setOption = (i: number, v: string) => setOptions((os) => os.map((o, k) => (k === i ? v : o)));

  const start = async () => {
    const spec = { question, options, durationSeconds: duration, mirrorTwitch: mirror };
    const p = pollProblem(spec);
    if (p) {
      setProblem(t(p, { max: POLL_LIMITS.maxOption, maxQuestion: POLL_LIMITS.maxQuestion,
        min: POLL_LIMITS.minDuration, maxDuration: POLL_LIMITS.maxDuration }));
      return;
    }
    setProblem(null);
    setBusy(true);
    try {
      await api.startPoll(spec);
      toast.success(t("polls.started"));
      setQuestion("");
      setOptions(["", ""]);
      await onStarted();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("polls.startFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="flex flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        void start();
      }}
    >
      <Label htmlFor="poll-question" className="text-sm">
        {t("polls.question")}
      </Label>
      <input
        id="poll-question"
        value={question}
        maxLength={POLL_LIMITS.maxQuestion}
        onChange={(e) => setQuestion(e.target.value)}
        placeholder={t("polls.questionPlaceholder")}
        className="h-7 rounded border border-border bg-card-raised px-2 text-sm"
      />
      <span className="text-sm">{t("polls.options")}</span>
      {options.map((o, i) => (
        <div key={i} className="flex items-center gap-1">
          <span className="tnum w-4 text-sm text-muted-foreground">{i + 1}</span>
          <input
            value={o}
            maxLength={POLL_LIMITS.maxOption}
            onChange={(e) => setOption(i, e.target.value)}
            aria-label={t("polls.optionAria", { n: i + 1 })}
            className="h-7 min-w-0 flex-1 rounded border border-border bg-card-raised px-2 text-sm"
          />
          {options.length > POLL_LIMITS.minOptions && (
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="h-7 w-7"
              aria-label={t("polls.removeOption", { n: i + 1 })}
              onClick={() => setOptions((os) => os.filter((_, k) => k !== i))}
            >
              <X className="h-3 w-3" />
            </Button>
          )}
        </div>
      ))}
      {options.length < POLL_LIMITS.maxOptions && (
        <Button type="button" size="sm" variant="ghost" className="self-start" onClick={() => setOptions((os) => [...os, ""])}>
          <Plus className="h-3 w-3" /> {t("polls.addOption")}
        </Button>
      )}
      <div className="flex items-center gap-1.5">
        <Label htmlFor="poll-duration" className="text-sm">
          {t("polls.duration")}
        </Label>
        <input
          id="poll-duration"
          type="number"
          min={POLL_LIMITS.minDuration}
          max={POLL_LIMITS.maxDuration}
          value={duration}
          onChange={(e) => setDuration(Math.round(Number(e.target.value) || 0))}
          className="tnum h-7 w-20 rounded border border-border bg-card-raised px-1 text-sm"
        />
        <span className="text-sm text-muted-foreground">{t("polls.seconds")}</span>
      </div>
      <div className="flex items-center gap-1.5">
        <input
          id="poll-mirror"
          type="checkbox"
          checked={mirror}
          disabled={busy}
          onChange={(e) => setMirror(e.target.checked)}
          className="h-3.5 w-3.5 accent-primary"
        />
        <Label htmlFor="poll-mirror" className="text-sm">
          {t("polls.mirrorTwitch")}
        </Label>
      </div>
      <span className="text-sm text-muted-foreground">{t("polls.voteHint")}</span>
      {problem && (
        <p role="alert" className="text-sm text-warn">
          {problem}
        </p>
      )}
      <Button type="submit" size="sm" disabled={busy} className="self-start">
        {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <BarChart3 className="h-3 w-3" />} {t("polls.start")}
      </Button>
    </form>
  );
}

function stateLine(t: Translator, p: Poll, now: number): string {
  if (p.state === "closing") return t("polls.closing");
  return t("polls.timeLeft", { time: clock(secondsLeft(p, now)) });
}

function LivePoll({ poll, now, onChanged }: { poll: Poll; now: number; onChanged: () => Promise<void> }) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const pct = sharePercents(poll);

  const end = async () => {
    setBusy(true);
    try {
      await api.endPoll(poll.id);
      toast.success(t("polls.ended"));
      await onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("polls.endFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5" data-poll-id={poll.id}>
      <div className="flex items-start justify-between gap-2">
        <span className="text-sm font-semibold">{poll.question}</span>
        <span className="tnum shrink-0 font-mono text-sm text-muted-foreground" data-testid="poll-state">
          {stateLine(t, poll, now)}
        </span>
      </div>
      <ul className="flex flex-col gap-1">
        {poll.options.map((o, i) => (
          <li key={`${i}-${o.label}`} className="flex flex-col gap-0.5">
            <div className="flex justify-between gap-2 text-sm">
              <span>{`${i + 1}. ${o.label}`}</span>
              <span className="tnum font-mono">{t("polls.optionCount", { votes: o.votes, pct: pct[i] })}</span>
            </div>
            <div className="h-1.5 overflow-hidden rounded bg-muted">
              <div className="h-full bg-primary" style={{ width: `${pct[i]}%` }} />
            </div>
            {platformsByVotes(o.byPlatform).length > 0 && (
              <div className="flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
                {platformsByVotes(o.byPlatform).map(([p, n]) => (
                  <span key={p} className={accentFor(p as ChatPlatform).text}>
                    {t("polls.platformCount", { platform: platformLabel(p), count: n })}
                  </span>
                ))}
              </div>
            )}
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-x-2 text-sm text-muted-foreground" data-testid="poll-total">
        <span>{t("polls.total", { count: poll.total })}</span>
        {platformsByVotes(poll.byPlatform).map(([p, n]) => (
          <span key={p}>{t("polls.platformCount", { platform: platformLabel(p), count: n })}</span>
        ))}
      </div>
      <Delivery poll={poll} />
      <Button size="sm" variant="outline" className="self-start" disabled={busy || poll.state !== "open"} onClick={() => void end()}>
        {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />} {t("polls.endNow")}
      </Button>
    </div>
  );
}

/** Where the poll was announced, and what became of the native mirror. */
function Delivery({ poll }: { poll: Poll }) {
  const t = useT();
  const announced = poll.announced ?? [];
  const native = poll.native ?? [];
  if (announced.length === 0 && native.length === 0) return null;
  return (
    <ul className="flex flex-col gap-0.5 text-[11px] text-muted-foreground" data-testid="poll-delivery">
      {announced.map((a, i) => (
        <li key={`a-${i}`} className={a.ok ? undefined : "text-warn"}>
          {a.ok
            ? t("polls.announcedOk", { platform: platformLabel(a.platform) })
            : a.skipped
              ? t("polls.announcedSkipped", { platform: platformLabel(a.platform) })
              : t("polls.announcedFailed", { platform: platformLabel(a.platform || "?"), detail: a.detail ?? "" })}
        </li>
      ))}
      {native.map((n, i) => (
        <li key={`n-${i}`} className={n.status === "failed" ? "text-warn" : undefined}>
          {n.status === "mirrored"
            ? t("polls.nativeMirrored", { platform: platformLabel(n.platform) })
            : t("polls.nativeNot", { platform: platformLabel(n.platform), reason: n.reason ?? "" })}
        </li>
      ))}
    </ul>
  );
}

function HistoryRow({ poll }: { poll: Poll }) {
  const t = useT();
  const best = Math.max(0, ...poll.options.map((o) => o.votes));
  const winners = best > 0 ? poll.options.filter((o) => o.votes === best).map((o) => o.label) : [];
  let summary: string;
  if (poll.state === "interrupted") summary = t("polls.interrupted");
  else if (winners.length === 0) summary = t("polls.noVotes");
  else summary = t("polls.winner", { label: winners.join(", "), count: poll.total });
  return (
    <li className="flex items-start justify-between gap-2 rounded border border-border px-1.5 py-1 text-sm">
      <div className="flex min-w-0 flex-col">
        <span className="truncate">{poll.question}</span>
        <span className="text-[11px] text-muted-foreground">{summary}</span>
      </div>
      <a
        className="inline-flex shrink-0 items-center gap-1 text-[11px] text-primary underline-offset-2 hover:underline"
        href={exportHref(poll.id)}
        download
        aria-label={t("polls.exportOneAria", { question: poll.question })}
      >
        <Download className="h-3 w-3" /> {t("polls.exportCsv")}
      </a>
    </li>
  );
}
