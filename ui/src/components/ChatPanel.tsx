import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { toast } from "sonner";
import {
  CornerUpLeft,
  Filter,
  Gamepad2,
  Loader2,
  MessagesSquare,
  MonitorPlay,
  Radio,
  Send,
  Shield,
  ThumbsUp,
  Trash2,
  Zap,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { StatusDot } from "@/components/signature/StatusDot";
import { api } from "@/lib/api";
import { clockTime, timestamp } from "@/lib/format";
import type { SignalTone } from "@/lib/signal";
import { cn } from "@/lib/utils";
import type {
  ChatLimit,
  ChatMessage,
  ChatPlatform,
  ChatSendResult,
  ChatStats,
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

/** How many messages the browser keeps. The server bounds its own ring and its
 *  table; this bounds the DOM so a twelve-hour stream does not end with fifty
 *  thousand nodes on the page. */
const CLIENT_LIMIT = 600;

/** Platforms get an accent from the theme's signal palette rather than from
 *  their own brand colours: the kit has five saturated colours and adding four
 *  more hex values for logos would break the one rule the design language has.
 *  The mapping is still mnemonic — YouTube red, Kick green, Facebook blue. */
const ACCENT: Record<
  ChatPlatform,
  {
    label: string;
    icon: React.ComponentType<{ className?: string }>;
    /** Left rule on a message row. */
    rule: string;
    text: string;
    chipOn: string;
  }
> = {
  youtube: {
    label: "YouTube",
    icon: MonitorPlay,
    rule: "border-l-down",
    text: "text-down",
    chipOn: "border-down/40 bg-down-dim text-down",
  },
  twitch: {
    label: "Twitch",
    icon: Gamepad2,
    rule: "border-l-primary",
    text: "text-primary",
    chipOn: "border-primary/50 bg-primary-dim text-foreground",
  },
  kick: {
    label: "Kick",
    icon: Zap,
    rule: "border-l-live",
    text: "text-live",
    chipOn: "border-live/40 bg-live-dim text-live",
  },
  facebook: {
    label: "Facebook",
    icon: ThumbsUp,
    rule: "border-l-armed",
    text: "text-armed",
    chipOn: "border-armed/40 bg-armed-dim text-armed",
  },
  custom: {
    label: "Custom",
    icon: Radio,
    rule: "border-l-border-strong",
    text: "text-muted-foreground",
    chipOn: "border-border-strong bg-secondary text-secondary-foreground",
  },
};

/** Unknown platforms fall back rather than crash: the server may know about a
 *  platform this build does not. */
export function accentFor(p: ChatPlatform) {
  return ACCENT[p] ?? ACCENT.custom;
}

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

export function chatStateTone(state: ChatStatus["state"]): SignalTone {
  switch (state) {
    case "live":
      return "live";
    case "connecting":
    case "degraded":
      return "warn";
    case "failed":
      return "down";
    default:
      return "idle";
  }
}

/** The sentence under a platform's name.
 *
 *  A red dot is not an explanation. Every state that is not plainly live gets
 *  words, and when the adapter did not supply any this says so rather than
 *  inventing a cause it cannot know. */
export function chatStateDetail(s: ChatStatus): string {
  if (s.detail) return s.detail;
  switch (s.state) {
    case "live":
      return "";
    case "connecting":
      return "Connecting.";
    case "degraded":
      return "Running with a limitation the platform did not name.";
    case "failed":
      return s.lastError || "Not connected, and the platform gave no reason.";
    default:
      return "Stopped.";
  }
}

// ------------------------------------------------------------- the live feed
//
// One socket for the whole app, refcounted, so mounting the dashboard panel and
// the chat page at once costs one connection rather than two. It is deliberately
// separate from useLiveData: chat is the only consumer of these two event types,
// and a page that never opens the pane should not pay to buffer its messages.

interface FeedState {
  connected: boolean;
  loading: boolean;
  /** False when the server has no chat hub wired at all. Distinct from an empty
   *  `statuses`, which means a hub is running with nothing attached. */
  configured: boolean;
  /** The scrollback came out of the database rather than a live connection. */
  stored: boolean;
  statuses: ChatStatus[];
  limits: ChatLimit[];
  messages: ChatMessage[];
  /** The hub's own counters. Only the overview carries them, so this is a
   *  snapshot from the last load rather than a live figure — the diagnostics
   *  block it feeds is refreshed by the reload button, not by every message. */
  stats: ChatStats | null;
  error: string;
}

const EMPTY: FeedState = {
  connected: false,
  loading: true,
  configured: false,
  stored: false,
  statuses: [],
  limits: [],
  messages: [],
  stats: null,
  error: "",
};

let feed: FeedState = EMPTY;
const listeners = new Set<() => void>();
let refs = 0;
let socket: WebSocket | null = null;
let retries = 0;
let reconnectTimer: number | undefined;
/** Dedupe across the socket and the initial fetch: the two overlap by exactly
 *  the messages that arrived while the fetch was in flight. */
let seen = new Set<string>();

function emit(patch: Partial<FeedState>) {
  feed = { ...feed, ...patch };
  listeners.forEach((l) => l());
}

const messageKey = (m: ChatMessage) => `${m.platform}\u0000${m.account ?? ""}\u0000${m.id}`;

function appendMessage(m: ChatMessage) {
  const key = messageKey(m);
  if (seen.has(key)) return;
  seen.add(key);
  const next = [...feed.messages, m];
  emit({ messages: next.length > CLIENT_LIMIT ? next.slice(next.length - CLIENT_LIMIT) : next });
}

/** Drop one message locally after the platform accepted a delete. The platform
 *  is the authority; this only keeps our copy from outliving it on screen. */
function forgetMessage(m: { platform: ChatPlatform; account?: string; id: string }) {
  const key = `${m.platform}\u0000${m.account ?? ""}\u0000${m.id}`;
  seen.delete(key);
  emit({ messages: feed.messages.filter((x) => messageKey(x) !== key) });
}

async function loadHistory() {
  try {
    const view = await api.chatOverview(CLIENT_LIMIT);
    seen = new Set(view.messages.map(messageKey));
    // Anything the socket delivered while this was in flight is kept: the
    // fetch is the floor of the scrollback, not the whole of it.
    const live = feed.messages.filter((m) => !seen.has(messageKey(m)));
    live.forEach((m) => seen.add(messageKey(m)));
    emit({
      loading: false,
      error: "",
      configured: view.configured,
      stored: view.stored ?? false,
      statuses: view.statuses ?? [],
      limits: view.limits ?? [],
      stats: view.stats ?? null,
      messages: [...view.messages, ...live],
    });
  } catch (err) {
    emit({
      loading: false,
      error: err instanceof Error ? err.message : "Could not load chat.",
    });
  }
}

function openSocket() {
  if (socket || refs === 0) return;
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`);
  socket = ws;

  ws.onopen = () => {
    retries = 0;
    emit({ connected: true });
  };
  ws.onmessage = (ev) => {
    let msg: { type: string; data: unknown };
    try {
      msg = JSON.parse(ev.data as string) as { type: string; data: unknown };
    } catch {
      return;
    }
    if (msg.type === "chat") {
      appendMessage(msg.data as ChatMessage);
    } else if (msg.type === "chatState") {
      // A state event is proof a hub exists, whatever the last overview said.
      emit({ statuses: (msg.data as ChatStatus[]) ?? [], configured: true, stored: false });
    }
  };
  ws.onclose = () => {
    socket = null;
    emit({ connected: false });
    if (refs === 0) return;
    // Same backoff shape the rest of the app uses, so a restarting server is
    // not hammered by every open tab.
    const delay = Math.min(1000 * 2 ** retries, 15000);
    retries++;
    reconnectTimer = window.setTimeout(openSocket, delay);
  };
  ws.onerror = () => ws.close();
}

function acquire() {
  refs++;
  if (refs === 1) {
    feed = { ...EMPTY };
    seen = new Set();
    openSocket();
    void loadHistory();
  }
}

function release() {
  refs--;
  if (refs > 0) return;
  window.clearTimeout(reconnectTimer);
  const ws = socket;
  socket = null;
  ws?.close();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The shared chat feed. Refcounted: the first component to mount opens the
 *  socket, the last to unmount closes it. */
export function useChatFeed() {
  useEffect(() => {
    acquire();
    return release;
  }, []);
  const state = useSyncExternalStore(subscribe, () => feed);

  const reload = useCallback(() => {
    emit({ loading: true });
    return loadHistory();
  }, []);

  const remove = useCallback(async (m: ChatMessage) => {
    // The button is offered for every message on every platform. Whether a
    // platform supports deletion is the platform's answer to give, and hiding
    // the action on a guess would silently remove a moderator's only tool the
    // day a platform gains the capability.
    await api.deleteChatMessage({ platform: m.platform, account: m.account, id: m.id });
    forgetMessage(m);
  }, []);

  return { ...state, reload, remove };
}

// -------------------------------------------------------------- text + emotes

type Piece = { kind: "text"; text: string } | { kind: "emote"; name: string; url?: string };

/** Split a message into text runs and emotes.
 *
 *  Offsets are RUNE offsets with an exclusive end — the convention every
 *  adapter normalises into — so the text is split on code points, not on UTF-16
 *  units. A message with an emoji before an emote misplaces every following
 *  range otherwise. Anything out of range is skipped rather than throwing: a
 *  line rendered without its emote beats a line not rendered at all. */
export function splitMessage(m: ChatMessage): Piece[] {
  const runes = Array.from(m.text);
  const emotes = (m.emotes ?? [])
    .filter((e) => e.start >= 0 && e.end > e.start && e.end <= runes.length)
    .sort((a, b) => a.start - b.start);

  const out: Piece[] = [];
  let at = 0;
  for (const e of emotes) {
    if (e.start < at) continue; // overlapping ranges: keep the first
    if (e.start > at) out.push({ kind: "text", text: runes.slice(at, e.start).join("") });
    out.push({
      kind: "emote",
      name: e.name || runes.slice(e.start, e.end).join(""),
      url: e.url,
    });
    at = e.end;
  }
  if (at < runes.length) out.push({ kind: "text", text: runes.slice(at).join("") });
  return out;
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

/** Every platform that has either spoken or is attached, in a stable order so
 *  the filter chips do not reshuffle as messages arrive. */
export function platformsIn(messages: ChatMessage[], statuses: ChatStatus[]): ChatPlatform[] {
  const order = Object.keys(ACCENT) as ChatPlatform[];
  const present = new Set<ChatPlatform>();
  statuses.forEach((s) => present.add(s.platform));
  messages.forEach((m) => present.add(m.platform));
  const known = order.filter((p) => present.has(p));
  const unknown = [...present]
    .filter((p) => !order.includes(p))
    .sort((a, b) => a.localeCompare(b));
  return [...known, ...unknown];
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
