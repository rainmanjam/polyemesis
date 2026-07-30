import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  CornerUpLeft,
  Filter,
  Loader2,
  MessagesSquare,
  Send,
  Shield,
  Trash2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { StatusDot } from "@/components/signature/StatusDot";
import { api } from "@/lib/api";
import { clockTime, timestamp } from "@/lib/format";
import type { SignalTone } from "@/lib/signal";
import { cn } from "@/lib/utils";
// Presentation and text-splitting live in lib/chat.ts; the refcounted socket in
// hooks/useChatFeed.ts. See either file for why they are not in here.
import {
  accentFor,
  chatStateDetail,
  chatStateTone,
  platformsIn,
  splitMessage,
} from "@/lib/chat";
import { messageKey, useChatFeed } from "@/hooks/useChatFeed";
import type {
  ChatLimit,
  ChatMessage,
  ChatPlatform,
  ChatSendResult,
  ChatStatus,
} from "@/lib/types";

// The unified chat pane.
//
// One timeline, four platforms, and the whole design bet is that you should
// never have to read a label to know where a line came from: every platform
// owns an accent and an icon, and the accent is on the message itself rather
// than in a legend somewhere above it.
//
// Nothing here re-derives a fact the server already stated. Connection state,
// send verdicts and delete permission all arrive from internal/chat, because a
// TypeScript guess about whether Twitch can delete a message is exactly the
// kind of check that is wrong in the restrictive direction.

/** Author colours when the platform sent none. Theme tokens, hashed off the
 *  name so one person keeps one colour for the whole broadcast. */
const NAME_TONES = [
  "text-primary",
  "text-armed",
  "text-live",
  "text-warn",
  "text-secondary-foreground",
];

function nameTone(name: string): string {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return NAME_TONES[h % NAME_TONES.length];
}

function MessageBody({ m }: { m: ChatMessage }) {
  const pieces = useMemo(() => splitMessage(m), [m]);
  return (
    <>
      {pieces.map((p, i) =>
        p.kind === "text" ? (
          <span key={i}>{p.text}</span>
        ) : p.url ? (
          <img
            key={i}
            src={p.url}
            alt={p.name}
            title={p.name}
            className="mx-0.5 inline-block h-4 align-text-bottom"
          />
        ) : (
          // No image URL from the platform: the name is what the sender typed
          // and reads correctly on its own.
          <span key={i} className="text-muted-foreground">
            {p.name}
          </span>
        ),
      )}
    </>
  );
}

// ------------------------------------------------------------------ the rows

function MessageRow({
  m,
  onDelete,
  compact,
}: {
  m: ChatMessage;
  onDelete?: (m: ChatMessage) => void;
  compact?: boolean;
}) {
  const accent = accentFor(m.platform);
  const Icon = accent.icon;
  const [busy, setBusy] = useState(false);

  const del = async () => {
    if (!onDelete) return;
    setBusy(true);
    try {
      await onDelete(m);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className={cn(
        "group relative border-l-2 py-0.5 pl-2 pr-6 text-[12px] leading-snug hover:bg-card-raised",
        accent.rule,
        m.echo && "bg-primary-dim/40",
      )}
    >
      <span className="mr-1.5 inline-flex items-center gap-1 align-baseline">
        <Icon className={cn("h-3 w-3 shrink-0", accent.text)} aria-label={accent.label} />
        {!compact && (
          <span className="tnum font-mono text-[10px] text-subtle-foreground" title={timestamp(m.at)}>
            {clockTime(m.at)}
          </span>
        )}
      </span>

      {m.replyTo && (
        <span className="mr-1 inline-flex items-center gap-0.5 text-[10px] text-subtle-foreground">
          <CornerUpLeft className="h-2.5 w-2.5" />
          {m.replyTo}
        </span>
      )}

      {m.author.moderator && (
        <Shield className="mr-0.5 inline h-3 w-3 align-text-bottom text-armed" aria-label="Moderator" />
      )}

      <span
        className={cn("font-semibold", !m.author.color && nameTone(m.author.name))}
        style={m.author.color ? { color: m.author.color } : undefined}
        title={(m.author.badges ?? []).map((b) => b.label || b.id).join(", ") || undefined}
      >
        {m.author.name}
      </span>
      <span className="text-subtle-foreground">{m.action ? " " : ": "}</span>
      <span
        className={cn(
          "break-words",
          m.action && "italic",
          m.action && !m.author.color && nameTone(m.author.name),
        )}
        style={m.action && m.author.color ? { color: m.author.color } : undefined}
      >
        <MessageBody m={m} />
      </span>

      {onDelete && (
        <button
          type="button"
          onClick={() => void del()}
          disabled={busy}
          aria-label={`Delete message from ${m.author.name}`}
          title="Delete on the platform it came from"
          className="absolute right-1 top-0.5 hidden h-4 w-4 items-center justify-center rounded text-subtle-foreground hover:text-down group-hover:flex"
        >
          {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />}
        </button>
      )}
    </div>
  );
}

/** The merged timeline. Pinned to the bottom unless the reader has scrolled up,
 *  because yanking somebody back to the newest line while they are reading an
 *  older one is the single most common way a chat pane becomes unusable. */
export function ChatTimeline({
  messages,
  onDelete,
  compact,
  empty,
}: {
  messages: ChatMessage[];
  onDelete?: (m: ChatMessage) => void;
  compact?: boolean;
  empty?: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pinned, setPinned] = useState(true);
  const [behind, setBehind] = useState(0);
  const countRef = useRef(messages.length);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setPinned(atBottom);
    if (atBottom) setBehind(0);
  }, []);

  useEffect(() => {
    const grew = messages.length - countRef.current;
    countRef.current = messages.length;
    if (pinned) {
      ref.current?.scrollTo({ top: ref.current.scrollHeight });
    } else if (grew > 0) {
      setBehind((n) => n + grew);
    }
  }, [messages, pinned]);

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={ref}
        onScroll={onScroll}
        className="h-full overflow-y-auto py-1"
        role="log"
        aria-live="polite"
        aria-relevant="additions"
      >
        {messages.length === 0
          ? empty
          : messages.map((m) => (
              <MessageRow
                key={messageKey(m)}
                m={m}
                onDelete={onDelete}
                compact={compact}
              />
            ))}
      </div>

      {behind > 0 && (
        <Button
          size="sm"
          variant="secondary"
          className="absolute bottom-2 left-1/2 -translate-x-1/2 shadow"
          onClick={() => {
            setPinned(true);
            setBehind(0);
            ref.current?.scrollTo({ top: ref.current.scrollHeight, behavior: "smooth" });
          }}
        >
          {behind} new
        </Button>
      )}
    </div>
  );
}

// ------------------------------------------------------------------- filters

/** Platform toggles. Every platform that has spoken or is connected gets a chip;
 *  turning one off hides it from the timeline and nothing else. */
export function PlatformFilter({
  platforms,
  hidden,
  onToggle,
  counts,
}: {
  platforms: ChatPlatform[];
  hidden: Set<ChatPlatform>;
  onToggle: (p: ChatPlatform) => void;
  counts?: Map<ChatPlatform, number>;
}) {
  if (platforms.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1">
      <Filter className="h-3 w-3 text-subtle-foreground" aria-hidden />
      {platforms.map((p) => {
        const accent = accentFor(p);
        const Icon = accent.icon;
        const on = !hidden.has(p);
        return (
          <button
            key={p}
            type="button"
            onClick={() => onToggle(p)}
            aria-pressed={on}
            className={cn(
              "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider transition-colors",
              on ? accent.chipOn : "border-border text-subtle-foreground hover:text-muted-foreground",
            )}
          >
            <Icon className="h-3 w-3" />
            {accent.label}
            {counts?.get(p) ? <span className="tnum font-mono">{counts.get(p)}</span> : null}
          </button>
        );
      })}
    </div>
  );
}

// -------------------------------------------------------------- the composer

/** One line's worth of send verdict, per platform.
 *
 *  "Sent" is never the whole story: partial delivery is the normal case, and an
 *  operator who typed something important needs to know that Twitch has it and
 *  YouTube does not — not that "sending failed". */
function SendVerdict({ results }: { results: ChatSendResult[] }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px]">
      {results.map((r) => {
        const accent = accentFor(r.platform);
        const tone: SignalTone = r.ok ? "live" : r.skipped ? "idle" : "down";
        return (
          <span key={`${r.platform}:${r.account ?? ""}`} className="inline-flex items-center gap-1">
            <StatusDot tone={tone} size="sm" />
            <span className={accent.text}>{accent.label}</span>
            <span className="text-muted-foreground">
              {r.ok ? "sent" : r.skipped ? "cannot send" : "failed"}
              {r.detail ? ` — ${r.detail}` : ""}
            </span>
          </span>
        );
      })}
    </div>
  );
}

export function ChatComposer({
  statuses,
  limits,
  configured,
  compact,
}: {
  statuses: ChatStatus[];
  limits: ChatLimit[];
  configured: boolean;
  compact?: boolean;
}) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [results, setResults] = useState<ChatSendResult[] | null>(null);

  const senders = statuses.filter((s) => s.canSend);
  // The strictest published limit among the platforms that will actually
  // receive this. Advisory only: over it the counter turns amber and the send
  // button still works, because the platform is the authority on its own rules
  // and every one of them accepts a message we would have refused.
  const strictest = useMemo(() => {
    const caps = senders
      .map((s) => limits.find((l) => l.platform === s.platform)?.maxChars ?? 0)
      .filter((n) => n > 0);
    return caps.length ? Math.min(...caps) : 0;
  }, [senders, limits]);
  const strictestPlatform = useMemo(
    () =>
      senders.find(
        (s) => (limits.find((l) => l.platform === s.platform)?.maxChars ?? 0) === strictest,
      )?.platform,
    [senders, limits, strictest],
  );

  const over = strictest > 0 && [...text].length > strictest;

  const send = async () => {
    const body = text.trim();
    if (!body || sending) return;
    setSending(true);
    try {
      const res = await api.sendChat(body);
      setResults(res.results);
      setText("");
      if (res.failed > 0) {
        toast.error(
          res.sent > 0
            ? `Sent to ${res.sent}, failed on ${res.failed}.`
            : "No platform accepted the message.",
        );
      }
    } catch (err) {
      setResults(null);
      toast.error(err instanceof Error ? err.message : "Could not send.");
    } finally {
      setSending(false);
    }
  };

  if (!configured) {
    return (
      <p className="border-t border-border px-2 py-2 text-[11px] text-muted-foreground">
        Chat is not running on this server, so there is nothing to send to. Connect a platform
        account under Settings → Platform credentials.
      </p>
    );
  }

  return (
    <div className="shrink-0 border-t border-border p-2">
      {results && (
        <div className="mb-1.5">
          <SendVerdict results={results} />
        </div>
      )}
      <div className="flex items-end gap-1.5">
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter breaks the line. The other way round
            // makes a chat box feel like a form.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
          rows={compact ? 1 : 2}
          placeholder={
            senders.length === 0
              ? "No connected platform can send right now — you can still type."
              : `Send to ${senders.map((s) => accentFor(s.platform).label).join(", ")}`
          }
          className="min-h-0 resize-none text-[12px]"
          aria-label="Message to send to every connected platform"
        />
        <Button size="sm" onClick={() => void send()} disabled={sending || !text.trim()}>
          {sending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Send className="h-3 w-3" />}
          {!compact && "Send"}
        </Button>
      </div>
      {strictest > 0 && (
        <p
          className={cn(
            "mt-1 text-[10px]",
            over ? "text-warn" : "text-subtle-foreground",
          )}
        >
          <span className="tnum font-mono">
            {[...text].length}/{strictest}
          </span>{" "}
          {over
            ? `${strictestPlatform ? accentFor(strictestPlatform).label : "One platform"} publishes a ${strictest}-character limit and will probably truncate or reject this. It will still be sent.`
            : `strictest limit among the connected platforms${strictestPlatform ? ` (${accentFor(strictestPlatform).label})` : ""}`}
        </p>
      )}
    </div>
  );
}

// --------------------------------------------------------------- status rail

/** Per-platform connection state, with the reason attached.
 *
 *  The whole point of this block is the sentence. "YouTube: quota exhausted,
 *  resumes 00:00" is actionable; a red dot is a puzzle. */
export function ChatStatusList({
  statuses,
  compact,
}: {
  statuses: ChatStatus[];
  compact?: boolean;
}) {
  if (statuses.length === 0) return null;
  return (
    <div className={cn("flex flex-col gap-1", compact && "gap-0.5")}>
      {statuses.map((s) => {
        const accent = accentFor(s.platform);
        const Icon = accent.icon;
        const detail = chatStateDetail(s);
        const quota = s.quota;
        return (
          <div
            key={`${s.platform}:${s.account ?? ""}`}
            className="flex items-start gap-2 rounded border border-border bg-card px-2 py-1.5"
          >
            <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", accent.text)} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <StatusDot tone={chatStateTone(s.state)} size="sm" />
                <span className="text-[12px] font-medium">{accent.label}</span>
                {s.channel && (
                  <span className="truncate text-[11px] text-muted-foreground">{s.channel}</span>
                )}
                <span className="ml-auto text-[10px] uppercase tracking-wider text-subtle-foreground">
                  {s.state}
                </span>
              </div>
              {detail && (
                <p className="mt-0.5 text-[11px] leading-snug text-muted-foreground">{detail}</p>
              )}
              {quota && (
                <p className="mt-0.5 text-[10px] leading-snug text-subtle-foreground">
                  <span className="tnum font-mono">
                    {quota.used.toLocaleString()}/{quota.limit.toLocaleString()}
                  </span>{" "}
                  estimated API units used
                  {quota.resetAt ? `, resets ${clockTime(quota.resetAt)}` : ""}
                  {quota.paused ? " — polling is paused until then" : ""}
                  {quota.intervalMs > 0
                    ? `, polling every ${Math.round(quota.intervalMs / 1000)}s`
                    : ""}
                </p>
              )}
              {!compact && (
                <p className="mt-0.5 text-[10px] text-subtle-foreground">
                  <span className="tnum font-mono">{s.received.toLocaleString()}</span> received ·{" "}
                  <span className="tnum font-mono">{s.sent.toLocaleString()}</span> sent
                  {s.restarts > 0 && (
                    <>
                      {" · "}
                      <span className="tnum font-mono">{s.restarts}</span> reconnect
                      {s.restarts === 1 ? "" : "s"}
                    </>
                  )}
                  {!s.canSend && " · receive-only"}
                </p>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// -------------------------------------------------------------- the panel

/** The compact pane, sized to sit beside the dashboard preview.
 *
 *  Same feed, same send box and same delete action as the full page — only the
 *  chrome is smaller. A second implementation would eventually disagree with
 *  the first about something that matters. */
export function ChatPanel({
  className,
  showComposer = true,
}: {
  className?: string;
  showComposer?: boolean;
}) {
  const { messages, statuses, limits, configured, loading, connected, stored, error, remove } =
    useChatFeed();
  const [hidden, setHidden] = useState<Set<ChatPlatform>>(new Set());

  const visible = useMemo(
    () => messages.filter((m) => !hidden.has(m.platform)),
    [messages, hidden],
  );
  const platforms = useMemo(() => platformsIn(messages, statuses), [messages, statuses]);

  const toggle = (p: ChatPlatform) =>
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  const del = useCallback(
    async (m: ChatMessage) => {
      try {
        await remove(m);
      } catch (err) {
        // The platform's own words — "polyemesis cannot delete twitch
        // messages; use the twitch dashboard" is the whole answer.
        toast.error(err instanceof Error ? err.message : "Could not delete that message.");
      }
    },
    [remove],
  );

  return (
    <div className={cn("flex min-h-0 flex-col rounded-md border border-border bg-card", className)}>
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-2 py-1.5">
        <MessagesSquare className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-[12px] font-semibold">Chat</span>
        {stored && (
          <Badge variant="outline" title="Nothing is connected; this is the stored scrollback.">
            history
          </Badge>
        )}
        {configured && !connected && (
          <Badge variant="warn" title="The live socket is closed; messages will resume when it reconnects.">
            offline
          </Badge>
        )}
        <div className="ml-auto">
          <PlatformFilter platforms={platforms} hidden={hidden} onToggle={toggle} />
        </div>
      </div>

      <ChatTimeline
        messages={visible}
        onDelete={del}
        compact
        empty={
          <ChatEmpty loading={loading} configured={configured} error={error} statuses={statuses} />
        }
      />

      {showComposer && (
        <ChatComposer statuses={statuses} limits={limits} configured={configured} compact />
      )}
    </div>
  );
}

/** What an empty timeline says. Four different situations, four different
 *  sentences — "no messages" would be true in all four and useful in none. */
export function ChatEmpty({
  loading,
  configured,
  error,
  statuses,
}: {
  loading: boolean;
  configured: boolean;
  error: string;
  statuses: ChatStatus[];
}) {
  let body: string;
  if (loading) body = "Loading chat…";
  else if (error) body = error;
  else if (!configured)
    body =
      "Chat is not running on this server. Connect a platform account under Settings → Platform credentials, then restart to attach it.";
  else if (statuses.length === 0)
    body = "Chat is running but no platform account is attached yet.";
  else body = "Connected and waiting. Nothing has been said yet.";

  return (
    <div className="flex h-full items-center justify-center px-6 text-center">
      <p className="max-w-sm text-[11px] leading-relaxed text-muted-foreground">
        {loading ? <Loader2 className="mr-1 inline h-3 w-3 animate-spin" /> : null}
        {body}
      </p>
    </div>
  );
}
