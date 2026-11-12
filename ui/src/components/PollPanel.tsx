import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { BarChart3, Download, Loader2, Plus, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { accentFor } from "@/lib/chat";
import { platformName } from "@/lib/platforms";
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
  type PollAnnouncement,
  type PollNative,
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

export function PollPanel({ twitchAvailable = false }: Readonly<{ twitchAvailable?: boolean }>) {
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
      {!unavailable && !live && (
        // The start form is the least-used part of the sidebar once a show is
        // running, and it is about seventeen controls. Behind a disclosure it
        // no longer sits above the connection status the moderator needs.
        <details className="rounded border border-border px-2 py-1">
          <summary className="cursor-pointer text-sm font-semibold">{t("polls.startDisclosure")}</summary>
          <div className="pt-2">
            <StartForm onStarted={load} twitchAvailable={twitchAvailable} />
          </div>
        </details>
      )}

      {finished.length > 0 && (
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm font-semibold">{t("polls.history")}</span>
            <a className="inline-flex items-center gap-1 text-sm text-primary-bright underline-offset-2 hover:underline" href={exportHref()} download>
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

/** An option row with a client-only key. The React key is never sent to the
 *  API: `start` maps the rows back to plain strings first. */
interface OptionRow {
  key: number;
  text: string;
}

let nextOptionKey = 0;

function newOptionRow(): OptionRow {
  nextOptionKey += 1;
  return { key: nextOptionKey, text: "" };
}

function StartForm({
  onStarted,
  twitchAvailable,
}: Readonly<{
  onStarted: () => Promise<void>;
  /** Whether a Twitch account is attached. Without one there is nothing to mirror to. */
  twitchAvailable: boolean;
}>) {
  const t = useT();
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState<OptionRow[]>(() => [newOptionRow(), newOptionRow()]);
  const [duration, setDuration] = useState(120);
  // On only when there is a Twitch account to mirror to. The old default was
  // "on" with no account, and the only sign of it was a line after start.
  const [mirror, setMirror] = useState(twitchAvailable);
  // Off by default: a YouTube live poll costs 40 units of the day's API quota.
  const [mirrorYouTube, setMirrorYouTube] = useState(false);
  // Off by default: Facebook only takes a poll on a video that is live now.
  const [mirrorFacebook, setMirrorFacebook] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const setOption = (i: number, v: string) =>
    setOptions((os) => os.map((o, k) => (k === i ? { ...o, text: v } : o)));

  const start = async () => {
    const spec = {
      question,
      options: options.map((o) => o.text),
      durationSeconds: duration,
      mirrorTwitch: twitchAvailable && mirror,
      mirrorYouTube,
      mirrorFacebook,
    };
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
      setOptions([newOptionRow(), newOptionRow()]);
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
        <div key={o.key} className="flex items-center gap-1">
          <span className="tnum w-4 text-sm text-muted-foreground">{i + 1}</span>
          <input
            value={o.text}
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
        <Button type="button" size="sm" variant="ghost" className="self-start" onClick={() => setOptions((os) => [...os, newOptionRow()])}>
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
      {twitchAvailable && (
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
      )}
      <div className="flex items-center gap-1.5">
        <input
          id="poll-mirror-youtube"
          type="checkbox"
          checked={mirrorYouTube}
          disabled={busy}
          onChange={(e) => setMirrorYouTube(e.target.checked)}
          className="h-3.5 w-3.5 accent-primary"
        />
        <Label htmlFor="poll-mirror-youtube" className="text-sm">
          {t("polls.mirrorYouTube")}
        </Label>
      </div>
      <div className="flex items-center gap-1.5">
        <input
          id="poll-mirror-facebook"
          type="checkbox"
          checked={mirrorFacebook}
          disabled={busy}
          onChange={(e) => setMirrorFacebook(e.target.checked)}
          className="h-3.5 w-3.5 accent-primary"
        />
        <Label htmlFor="poll-mirror-facebook" className="text-sm">
          {t("polls.mirrorFacebook")}
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

function LivePoll({ poll, now, onChanged }: Readonly<{ poll: Poll; now: number; onChanged: () => Promise<void> }>) {
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
              <span className="tnum font-mono">{t(o.votes === 1 ? "polls.optionCountOne" : "polls.optionCount", { votes: o.votes, pct: pct[i] })}</span>
            </div>
            <div className="h-1.5 overflow-hidden rounded bg-muted">
              <div className="h-full bg-primary" style={{ width: `${pct[i]}%` }} />
            </div>
            {platformsByVotes(o.byPlatform).length > 0 && (
              <div className="flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
                {platformsByVotes(o.byPlatform).map(([p, n]) => (
                  <span key={p} className={accentFor(p as ChatPlatform).text}>
                    {t("polls.platformCount", { platform: platformName(p), count: n })}
                  </span>
                ))}
              </div>
            )}
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-x-2 text-sm text-muted-foreground" data-testid="poll-total">
        <span>{t(poll.total === 1 ? "polls.totalOne" : "polls.total", { count: poll.total })}</span>
        {platformsByVotes(poll.byPlatform).map(([p, n]) => (
          <span key={p}>{t("polls.platformCount", { platform: platformName(p), count: n })}</span>
        ))}
      </div>
      <Delivery poll={poll} />
      <Button size="sm" variant="outline" className="self-start" disabled={busy || poll.state !== "open"} onClick={() => void end()}>
        {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />} {t("polls.endNow")}
      </Button>
    </div>
  );
}

function announcedText(t: Translator, a: PollAnnouncement): string {
  if (a.ok) return t("polls.announcedOk", { platform: platformName(a.platform) });
  if (a.skipped) return t("polls.announcedSkipped", { platform: platformName(a.platform) });
  return t("polls.announcedFailed", { platform: platformName(a.platform || "?"), detail: a.detail ?? "" });
}

function nativeText(t: Translator, n: PollNative): string {
  if (n.status === "mirrored") {
    return n.reason
      ? t("polls.nativeMirroredWhy", { platform: platformName(n.platform), reason: n.reason })
      : t("polls.nativeMirrored", { platform: platformName(n.platform) });
  }
  if (n.status === "stopped") return t("polls.nativeStopped", { platform: platformName(n.platform) });
  return t("polls.nativeNot", { platform: platformName(n.platform), reason: n.reason ?? "" });
}

/** Where the poll was announced, and what became of the native mirror. */
function Delivery({ poll }: Readonly<{ poll: Poll }>) {
  const t = useT();
  const announced = poll.announced ?? [];
  const native = poll.native ?? [];
  if (announced.length === 0 && native.length === 0) return null;
  return (
    <ul className="flex flex-col gap-0.5 text-[11px] text-muted-foreground" data-testid="poll-delivery">
      {announced.map((a, i) => (
        <li key={`${i}-${announcedText(t, a)}`} className={a.ok ? undefined : "text-warn"}>
          {announcedText(t, a)}
        </li>
      ))}
      {native.map((n, i) => (
        <li key={`${i}-${nativeText(t, n)}`} className={n.status === "failed" ? "text-warn" : undefined}>
          {nativeText(t, n)}
        </li>
      ))}
    </ul>
  );
}

function HistoryRow({ poll }: Readonly<{ poll: Poll }>) {
  const t = useT();
  const best = Math.max(0, ...poll.options.map((o) => o.votes));
  const winners = best > 0 ? poll.options.filter((o) => o.votes === best).map((o) => o.label) : [];
  let summary: string;
  if (poll.state === "interrupted") summary = t("polls.interrupted");
  else if (winners.length === 0) summary = t("polls.noVotes");
  else summary = t(poll.total === 1 ? "polls.winnerOne" : "polls.winner", { label: winners.join(", "), count: poll.total });
  return (
    <li className="flex items-start justify-between gap-2 rounded border border-border px-1.5 py-1 text-sm">
      <div className="flex min-w-0 flex-col">
        <span className="truncate">{poll.question}</span>
        <span className="text-[11px] text-muted-foreground">{summary}</span>
      </div>
      <a
        className="inline-flex shrink-0 items-center gap-1 text-[11px] text-primary-bright underline-offset-2 hover:underline"
        href={exportHref(poll.id)}
        download
        aria-label={t("polls.exportOneAria", { question: poll.question })}
      >
        <Download className="h-3 w-3" /> {t("polls.exportCsv")}
      </a>
    </li>
  );
}
