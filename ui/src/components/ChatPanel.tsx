import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import { toast } from "sonner";
import {
  CornerUpLeft,
  Filter,
  Loader2,
  MessagesSquare,
  Search,
  Send,
  Shield,
  Trash2,
  X,
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
import { useChatSearch, type ChatSearchState } from "@/hooks/useChatSearch";
import { ChatUserCard } from "@/components/ChatUserCard";
import { ChatMessageMenu, type MenuAnchor } from "@/components/ChatMessageMenu";
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
  onOpenUser,
  onMenu,
  compact,
}: {
  m: ChatMessage;
  onDelete?: (m: ChatMessage) => void;
  /** Open the moderator's user card for whoever said this. */
  onOpenUser?: (m: ChatMessage) => void;
  /** Open the quick-action menu at a point. */
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
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

  // Right-click is the moderator's reflex from every other chat tool, and
  // double-click is the same menu for anyone whose pointer has no right button
  // — a trackpad without secondary-click configured, or a touch device.
  //
  // Text selection is deliberately NOT sacrificed for this: the browser's own
  // selection survives because neither handler fires on a drag, and copying a
  // line to quote it elsewhere is something moderators do constantly.
  const openMenu = onMenu
    ? (e: React.MouseEvent) => {
        e.preventDefault();
        onMenu(m, { x: e.clientX, y: e.clientY });
      }
    : undefined;

  return (
    <div
      onContextMenu={openMenu}
      onDoubleClick={openMenu}
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

      {/* The name is the way in to moderation, which is where every chat tool
          puts it: you read a bad line, you click who said it. A separate
          moderation button per row would compete with Delete for the same
          corner and make the common case (read, judge, act) three clicks. */}
      <button
        type="button"
        disabled={!onOpenUser || !m.author.id}
        onClick={() => onOpenUser?.(m)}
        aria-label={m.author.id ? `Moderate ${m.author.name}` : m.author.name}
        title={
          m.author.id
            ? `What ${m.author.name} has said, and what to do about it`
            : // A platform that sent no author id cannot be addressed by any
              // moderation call, so the name is not a button. Saying why beats a
              // control that silently does nothing.
              `${m.author.name} — ${m.platform} sent no user id for this message, so it cannot be moderated`
        }
        className={cn(
          "font-semibold",
          !m.author.color && nameTone(m.author.name),
          onOpenUser && m.author.id
            ? "cursor-pointer hover:underline focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none"
            : "cursor-default",
        )}
        style={m.author.color ? { color: m.author.color } : undefined}
      >
        {m.author.name}
      </button>
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
  onOpenUser,
  onMenu,
  compact,
  empty,
}: {
  messages: ChatMessage[];
  onDelete?: (m: ChatMessage) => void;
  onOpenUser?: (m: ChatMessage) => void;
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
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
                onOpenUser={onOpenUser}
                onMenu={onMenu}
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

// -------------------------------------------------------------------- search

/** The search box. Narrow on purpose: one field, one clear button. */
export function ChatSearchBox({
  search,
  className,
}: {
  search: ChatSearchState;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1",
        className,
      )}
    >
      <Search className="h-3 w-3 shrink-0 text-subtle-foreground" aria-hidden />
      <input
        type="search"
        value={search.query}
        onChange={(e) => search.setQuery(e.target.value)}
        // Escape is the way out of a search box everywhere else, and a
        // moderator who has to find and click a small × while chat scrolls has
        // been given a worse tool than they had before.
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            search.clear();
          }
        }}
        placeholder="Search chat history…"
        aria-label="Search chat history"
        className="min-w-0 flex-1 bg-transparent text-[12px] outline-none placeholder:text-subtle-foreground"
      />
      {search.loading && <Loader2 className="h-3 w-3 shrink-0 animate-spin text-subtle-foreground" />}
      {search.query && (
        <button
          type="button"
          onClick={search.clear}
          aria-label="Clear search"
          className="shrink-0 rounded p-0.5 text-subtle-foreground hover:text-foreground"
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </div>
  );
}

/** Search results, in place of the live timeline.
 *
 *  Replacing the timeline rather than filtering it is the honest rendering:
 *  these messages come from the database and are frozen at the moment of the
 *  query, so letting live messages continue to append underneath them would
 *  present two different things as one list. */
export function ChatSearchResults({
  search,
  onDelete,
  onOpenUser,
  onMenu,
}: {
  search: ChatSearchState;
  onDelete?: (m: ChatMessage) => void;
  onOpenUser?: (m: ChatMessage) => void;
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
}) {
  const t = useT();
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 bg-card-raised px-2 py-1 text-[10px] text-subtle-foreground">
        <span>
          {search.loading
            ? "Searching…"
            : `${search.results.length} match${search.results.length === 1 ? "" : "es"}`}
          {search.truncated && " (showing the newest)"}
        </span>
        <span className="ml-auto">newest first</span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto py-1" role="log">
        {search.error ? (
          <p className="px-2 py-4 text-[12px] text-down">{search.error}</p>
        ) : search.results.length === 0 && !search.loading ? (
          // The caveat matters most exactly here. An empty result is the one
          // outcome an operator can misread as evidence, and "no matches" on
          // its own invites "then they never said it" — which is a conclusion
          // about a purged table, not about a person.
          <div className="px-2 py-4 text-[12px] text-muted-foreground">
            <p>{t("chatpage.noMatchesInScrollback")}</p>
            {search.note && (
              <p className="mt-1 text-[11px] text-subtle-foreground">{search.note}</p>
            )}
          </div>
        ) : (
          search.results.map((m) => (
            <MessageRow
              key={messageKey(m)}
              m={m}
              onDelete={onDelete}
              onOpenUser={onOpenUser}
              onMenu={onMenu}
            />
          ))
        )}
      </div>

      {search.results.length > 0 && search.note && (
        <p className="shrink-0 border-t border-border px-2 py-1 text-[10px] leading-snug text-subtle-foreground">
          {search.note}
        </p>
      )}
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
  const {
    messages,
    statuses,
    limits,
    configured,
    loading,
    connected,
    stored,
    error,
    frameError,
    remove,
  } = useChatFeed();
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

  // The message whose author's card is open. Holding the MESSAGE rather than
  // just an id keeps the platform and account with it, which the card needs to
  // address a moderation call and cannot re-derive.
  const [card, setCard] = useState<ChatMessage | null>(null);
  // Same reasoning for the quick menu, plus where it was opened.
  const [menu, setMenu] = useState<{ m: ChatMessage; at: MenuAnchor } | null>(null);
  const search = useChatSearch();

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
      {card && (
        <ChatUserCard
          platform={card.platform}
          account={card.account}
          authorId={card.author.id ?? ""}
          authorName={card.author.name}
          open
          onOpenChange={(o) => !o && setCard(null)}
        />
      )}
      {menu && (
        <ChatMessageMenu
          message={menu.m}
          anchor={menu.at}
          onClose={() => setMenu(null)}
          onOpenCard={setCard}
          onDelete={del}
        />
      )}
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
        {/* #13/#21: unlike a closed socket, a frame that failed to parse gives
            no other signal at all -- `connected` still reads true. */}
        {frameError && (
          <Badge
            variant="warn"
            title="A message could not be understood and was dropped. The timeline may be missing something."
          >
            frame error
          </Badge>
        )}
        <div className="ml-auto">
          <PlatformFilter platforms={platforms} hidden={hidden} onToggle={toggle} />
        </div>
      </div>

      <ChatSearchBox search={search} />

      {search.active ? (
        <ChatSearchResults
          search={search}
          onDelete={del}
          onOpenUser={setCard}
          onMenu={(m, at) => setMenu({ m, at })}
        />
      ) : (
        <ChatTimeline
          messages={visible}
          onDelete={del}
          onOpenUser={setCard}
          onMenu={(m, at) => setMenu({ m, at })}
          compact
          empty={
            <ChatEmpty
              loading={loading}
              configured={configured}
              error={error}
              statuses={statuses}
              hiddenMessages={messages.length - visible.length}
            />
          }
        />
      )}

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
  hiddenMessages = 0,
}: {
  loading: boolean;
  configured: boolean;
  error: string;
  statuses: ChatStatus[];
  /** How many messages the platform filter is holding back.
   *
   *  THE FIFTH SITUATION, and the reason this prop exists: this component was
   *  handed the FILTERED list and knew nothing about the filter, so with every
   *  talking platform switched off it said "Connected and waiting. Nothing has
   *  been said yet." over a scrollback that was full. The dimmed chip is on
   *  screen, so it is recoverable — but the sentence is still false, and it is
   *  false in the direction that makes a moderator stop looking.
   *
   *  Optional and zero by default: a caller that has no filter cannot be made
   *  to think about one. */
  hiddenMessages?: number;
}) {
  let body: string;
  if (loading) body = "Loading chat…";
  else if (error) body = error;
  else if (!configured)
    body =
      "Chat is not running on this server. Connect a platform account under Settings → Platform credentials, then restart to attach it.";
  else if (statuses.length === 0)
    body = "Chat is running but no platform account is attached yet.";
  // Ranked ABOVE "nothing has been said": when the filter is the reason the
  // pane is empty, it is the only reason worth printing.
  else if (hiddenMessages > 0)
    body = `The platform filter is hiding ${hiddenMessages} message${
      hiddenMessages === 1 ? "" : "s"
    }. Nothing else has been said. Turn a platform back on above to see them.`;
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
