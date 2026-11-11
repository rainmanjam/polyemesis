import { useViewerTags } from "@/hooks/useViewerTags";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT, useTN, type TranslationKey, type Translator } from "@/lib/i18n";
import { toast } from "sonner";
import {
  Check,
  CornerUpLeft,
  Filter,
  Loader2,
  Megaphone,
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
import { Checkbox } from "@/components/ui/checkbox";
import { EmptyState } from "@/components/EmptyState";
import { StatusDot } from "@/components/signature/StatusDot";
import { api, ApiError } from "@/lib/api";
import { clockTime, timestamp } from "@/lib/format";
import type { SignalTone } from "@/lib/signal";
import { useAuthorInk } from "@/hooks/useAppliedTheme";
import { cn } from "@/lib/utils";
// Presentation and text-splitting live in lib/chat.ts; the refcounted socket in
// hooks/useChatFeed.ts. See either file for why they are not in here.
import {
  accentFor,
  chatStateDetail,
  chatStateTone,
  platformsIn,
  splitMessage,
  type Piece,
} from "@/lib/chat";
import { messageKey, useChatFeed } from "@/hooks/useChatFeed";
import { useConfirm, type Asks } from "@/hooks/useConfirm";
import { ConfirmDeleteMessage } from "@/components/ConfirmDeleteMessage";
import { chatActionSupport } from "@/lib/chatModeration";
import { useChatSearch, type ChatSearchState } from "@/hooks/useChatSearch";
import { ChatUserCard } from "@/components/ChatUserCard";
import { ChatMessageMenu, type MenuAnchor } from "@/components/ChatMessageMenu";
import type {
  ChatLimit,
  ChatLiveView,
  ChatSendTarget,
  ChatMessage,
  ChatPlatform,
  ChatSendResult,
  ChatState,
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
  "text-primary-bright",
  "text-cat-cyan",
  "text-ok",
  "text-warn",
  "text-secondary-foreground",
];

/** The per-platform state badge. Shares the process-state words where they
 *  mean the same thing; a state the server adds later falls back to its id. */
const CHAT_STATE_KEY: Record<ChatState, TranslationKey> = {
  connecting: "chatpage.stateConnecting",
  live: "state.live",
  degraded: "chatpage.stateDegraded",
  failed: "state.failed",
  stopped: "state.stopped",
};

function nameTone(name: string): string {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0; // NOSONAR S7758: hashes UTF-16 units on purpose; codePointAt would shift every emoji name onto a different tone
  return NAME_TONES[h % NAME_TONES.length];
}

function MessagePiece({ p }: Readonly<{ p: Piece }>) {
  if (p.kind === "text") return <span>{p.text}</span>;
  if (p.url) {
    return (
      <img
        src={p.url}
        alt={p.name}
        title={p.name}
        className="mx-0.5 inline-block h-4 align-text-bottom"
      />
    );
  }
  // No image URL from the platform: the name is what the sender typed
  // and reads correctly on its own.
  return <span className="text-muted-foreground">{p.name}</span>;
}

function MessageBody({ m }: Readonly<{ m: ChatMessage }>) {
  const pieces = useMemo(() => splitMessage(m), [m]);
  return (
    <>
      {pieces.map((p, i) => (
        <MessagePiece key={i} p={p} />
      ))}
    </>
  );
}

function newCountLabel(t: ReturnType<typeof useT>, behind: number): string {
  if (behind <= 0) return "";
  return t(behind === 1 ? "chatpage.newCountOne" : "chatpage.newCount", { count: behind });
}

function verdictLabel(t: ReturnType<typeof useT>, r: ChatSendResult): string {
  if (r.ok) return t("chatpage.verdictSent");
  if (r.reason === "notLive") return t("chatpage.verdictNotLive");
  if (r.reason === "notConnected") return t("chatpage.verdictNotConnected");
  if (r.skipped) return t("chatpage.verdictCannotSend");
  return t("chatpage.verdictFailed");
}

function verdictTone(r: ChatSendResult): SignalTone {
  if (r.ok) return "live";
  if (r.skipped) return "idle";
  return "down";
}

function limitHint(
  t: ReturnType<typeof useT>,
  over: boolean,
  strictestPlatform: ChatPlatform | undefined,
  strictest: number,
): string {
  if (over) {
    return strictestPlatform
      ? t("chatpage.overLimit", {
          platform: accentFor(strictestPlatform).label,
          limit: strictest,
        })
      : t("chatpage.overLimitUnknown", { limit: strictest });
  }
  return strictestPlatform
    ? t("chatpage.strictestLimitOf", { platform: accentFor(strictestPlatform).label })
    : t("chatpage.strictestLimit");
}

type SearchArea = "error" | "empty" | "results";

function searchAreaFor(error: string, count: number, loading: boolean): SearchArea {
  if (error) return "error";
  if (count === 0 && !loading) return "empty";
  return "results";
}

function matchLabel(t: ReturnType<typeof useT>, loading: boolean, count: number): string {
  if (loading) return t("chatpage.searching");
  return t(count === 1 ? "chatpage.matchOne" : "chatpage.matchMany", { count });
}

// ------------------------------------------------------------------ the rows

function MessageRow({
  m,
  onDelete,
  onOpenUser,
  onMenu,
  compact,
}: Readonly<{
  m: ChatMessage;
  onDelete?: Asks<ChatMessage>;
  /** Open the moderator's user card for whoever said this. */
  onOpenUser?: (m: ChatMessage) => void;
  /** Open the quick-action menu at a point. */
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
  compact?: boolean;
}>) {
  const t = useT();
  const accent = accentFor(m.platform);
  const Icon = accent.icon;
  const [busy, setBusy] = useState(false);

  // Whether this platform can delete at all, asked the same way the context
  // menu asks it. Not `onDelete !== undefined`: the handler being wired says
  // the OPERATOR may delete, never that the PLATFORM can.
  const deletable = chatActionSupport(m.platform, "delete", t);

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

  // The keyboard route to the same menu. A row takes focus (Tab), and the menu
  // opens from the keys a keyboard user already has for "menu here": Enter, the
  // ContextMenu key, and Shift+F10. Keys pressed on the name or delete buttons
  // inside the row belong to those buttons, so they are ignored here.
  const openMenuFromKeyboard = onMenu
    ? (e: React.KeyboardEvent<HTMLDivElement>) => {
        if (e.target !== e.currentTarget) return;
        const menuKey =
          e.key === "Enter" || e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey);
        if (!menuKey) return;
        e.preventDefault();
        const r = e.currentTarget.getBoundingClientRect();
        onMenu(m, { x: r.left + 16, y: r.top + r.height / 2 });
      }
    : undefined;

  // The platform's colour for this author, darkened on the light theme.
  const authorColour = useAuthorInk(m.author.color);
  // The operator's tags on this viewer, from one list-wide lookup.
  const viewerTags = useViewerTags(m.platform, m.account, m.author.id);

  const renderBodyText = () => (
    <>
      {m.echo && (
        <span className="mr-1 inline-flex align-text-bottom" title={t("chatpage.echoLabel")}>
          <Megaphone className="h-3 w-3 text-primary-bright" aria-label={t("chatpage.echoLabel")} />
        </span>
      )}

      {m.replyTo && (
        <span className="mr-1 inline-flex items-center gap-0.5 text-sm text-muted-foreground">
          <CornerUpLeft className="h-2.5 w-2.5" />
          {m.replyTo}
        </span>
      )}

      {m.author.moderator && (
        <Shield className="mr-0.5 inline h-3 w-3 align-text-bottom text-cat-cyan" aria-label={t("chatpage.moderator")} />
      )}

      {/* The name is the way in to moderation, which is where every chat tool
          puts it: you read a bad line, you click who said it. A separate
          moderation button per row would compete with Delete for the same
          corner and make the common case (read, judge, act) three clicks. */}
      <button
        type="button"
        disabled={!onOpenUser || !m.author.id}
        onClick={() => onOpenUser?.(m)}
        aria-label={m.author.id ? t("chatpage.moderateUser", { name: m.author.name }) : m.author.name}
        title={
          m.author.id
            ? t("chatpage.moderateUserTitle", { name: m.author.name })
            : // A platform that sent no author id cannot be addressed by any
              // moderation call, so the name is not a button. Saying why beats a
              // control that silently does nothing.
              t("chatpage.noUserId", { name: m.author.name, platform: m.platform })
        }
        className={cn(
          "font-semibold",
          !m.author.color && nameTone(m.author.name),
          onOpenUser && m.author.id
            ? "cursor-pointer hover:underline"
            : "cursor-default",
        )}
        style={authorColour ? { color: authorColour } : undefined}
      >
        {m.author.name}
      </button>
      {viewerTags.length > 0 && (
        <span
          className="ml-1 rounded border border-border px-1 align-baseline text-xs text-muted-foreground"
          title={t("viewerCard.tagBadgeTitle", { tags: viewerTags.join(", ") })}
          data-testid="viewer-tag-badge"
        >
          {viewerTags[0]}
          {viewerTags.length > 1 ? ` +${viewerTags.length - 1}` : ""}
        </span>
      )}
      <span className="text-muted-foreground">{m.action ? " " : ": "}</span>
      <span
        className={cn(
          "break-words",
          m.action && "italic",
          m.action && !m.author.color && nameTone(m.author.name),
        )}
        style={m.action && authorColour ? { color: authorColour } : undefined}
      >
        <MessageBody m={m} />
      </span>
    </>
  );
  const renderDeleteControl = () => (
    <>
      {/* THE SAME GATE THE MENU USES, on the icon that does the same thing.
          
          `{onDelete && …}` alone was the defect fixed in ChatMessageMenu, and
          this row icon kept it: Rumble publishes no delete API, so the trash
          can appeared on every Rumble line, opened a confirmation promising
          removal, and then failed against the server. Adding the confirmation
          without adding the gate made the failure MORE convincing, not less.
          
          Disabled-with-the-reason rather than hidden, which is what this file
          already does one control up: "Saying why beats a control that
          silently does nothing." A moderator who cannot find the delete has to
          be told the platform has none, or they go looking for their mistake. */}
      {onDelete && (
        <button
          type="button"
          onClick={() => void del()}
          disabled={busy || !deletable.ok}
          aria-label={t("chatpage.deleteFrom", { name: m.author.name })}
          title={deletable.ok ? t("chatpage.deleteOnPlatform") : deletable.reason}
          className="absolute right-1 top-0.5 hidden h-4 w-4 items-center justify-center rounded text-muted-foreground hover:text-down disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:text-muted-foreground group-hover:flex group-focus-within:flex pointer-coarse:flex"
        >
          {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />}
        </button>
      )}
    </>
  );

  return (
    <div // NOSONAR S6847: a message is a keyboard stop inside the role="log" chat list so Enter / Shift+F10 / the Menu key can open its moderation context menu, the keyboard twin of right-click; no native element offers that
      role="group"
      aria-roledescription={t("chatpage.messageRole")}
      tabIndex={onMenu ? 0 : undefined} // NOSONAR S6845: each message must be focusable for a keyboard moderator to reach its context menu
      onKeyDown={openMenuFromKeyboard}
      onContextMenu={openMenu}
      onDoubleClick={openMenu}
      className={cn(
        // A two-column grid is the hanging indent: the glyph (and the clock,
        // off the dock) sit in the first column and every wrapped line of the
        // message stays in the second, under the name, instead of running back
        // beneath the icon.
        "group relative grid grid-cols-[auto_minmax(0,1fr)] gap-x-1.5 border-l-2 py-0.5 pl-2 pr-6 text-[12px] leading-snug hover:bg-card-raised focus-visible:bg-card-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
        accent.rule,
        m.echo && "bg-primary-dim/40",
      )}
    >
      <span className="inline-flex h-lh items-center gap-1">
        <Icon className={cn("h-3 w-3 shrink-0", accent.text)} aria-label={accent.label} />
        {!compact && (
          <span className="tnum font-mono text-sm text-muted-foreground" title={timestamp(m.at)}>
            {clockTime(m.at)}
          </span>
        )}
      </span>

      <div className="min-w-0 [overflow-wrap:anywhere]">
        {renderBodyText()}
      </div>

      {/* The dock drops the clock column to keep lines wide; it comes back on
          hover or keyboard focus, laid over the gutter beside the delete icon. */}
      {compact && (
        <span
          className="tnum pointer-events-none absolute right-6 top-0.5 hidden rounded bg-card-raised px-1 font-mono text-sm text-muted-foreground group-hover:block group-focus-visible:block group-focus-within:block"
          title={timestamp(m.at)}
        >
          {clockTime(m.at)}
        </span>
      )}

      {renderDeleteControl()}
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
}: Readonly<{
  messages: ChatMessage[];
  onDelete?: Asks<ChatMessage>;
  onOpenUser?: (m: ChatMessage) => void;
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
  compact?: boolean;
  empty?: React.ReactNode;
}>) {
  const t = useT();
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
        // The log stays silent. Announcing every line on a busy stream is
        // noise a screen reader user cannot stop; the status region below
        // announces the one thing that matters while scrolled up.
        aria-live="off"
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

      <div role="status" aria-live="polite" className="sr-only">
        {newCountLabel(t, behind)}
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
          {t(behind === 1 ? "chatpage.newCountOne" : "chatpage.newCount", { count: behind })}
        </Button>
      )}
    </div>
  );
}

// ------------------------------------------------------------------- filters

/** The one chip shape the header filters and the composer's "Sending to" share,
 *  so the two rows read as the same control. Fixed height and no wrapping: a
 *  row of these scrolls sideways inside its own strip instead of growing taller.
 *  The ring is inset-safe because the strips pad their edges. */
const CHIP =
  "inline-flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-md border px-1.5 text-sm font-medium leading-none transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** A horizontally scrolling strip for chips. Scrolling rather than a "Filter (n)"
 *  popover: a platform toggle is a one-tap, glanceable state ("is Kick hidden?"),
 *  and a popover would bury that behind a second tap on the busiest surface in
 *  the console. At the widths the panel takes (360px up) the glyph-only chips
 *  fit without scrolling for the six platforms; the scroll is the safety net for
 *  an unusually long list, not the normal case. */
const STRIP =
  "flex min-w-0 flex-nowrap items-center gap-1 overflow-x-auto overscroll-x-contain px-0.5 py-0.5 [scrollbar-width:thin]";

/** Platform toggles. Every platform that has spoken or is connected gets a chip;
 *  turning one off hides it from the timeline and nothing else.
 *
 *  Three states, none carried by colour alone: on (the platform's own tint),
 *  off (plain, glyph dimmed), and a dashed border for a platform that has
 *  spoken but has no connected account right now. The word is hidden below the
 *  container's @xl width and left to the accessible name, so the glyph alone
 *  carries the chip on a narrow panel. */
export function PlatformFilter({
  platforms,
  hidden,
  onToggle,
  counts,
  attached,
  className,
}: Readonly<{
  platforms: ChatPlatform[];
  hidden: Set<ChatPlatform>;
  onToggle: (p: ChatPlatform) => void;
  counts?: Map<ChatPlatform, number>;
  /** Platforms with a connected account. A platform outside it is drawn dashed.
   *  Omitted, every chip is drawn as attached. */
  attached?: Set<ChatPlatform>;
  className?: string;
}>) {
  const t = useT();
  if (platforms.length === 0) return null;
  return (
    <div className={cn(STRIP, className)}>
      <Filter className="mr-0.5 hidden h-3 w-3 shrink-0 text-muted-foreground @xl:block" aria-hidden />
      {platforms.map((p) => {
        const accent = accentFor(p);
        const Icon = accent.icon;
        const on = !hidden.has(p);
        const noAccount = attached !== undefined && !attached.has(p);
        return (
          <button
            key={p}
            type="button"
            onClick={() => onToggle(p)}
            aria-pressed={on}
            title={noAccount ? `${accent.label}: ${t("chatpage.verdictNotConnected")}` : accent.label}
            className={cn(
              CHIP,
              on ? accent.chipOn : "border-border text-muted-foreground hover:bg-card-raised hover:text-foreground",
              noAccount && "border-dashed",
            )}
          >
            <Icon className={cn("h-3.5 w-3.5", !on && "opacity-60")} />
            <span className="sr-only @xl:not-sr-only">{accent.label}</span>
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
function SendVerdict({ results }: Readonly<{ results: ChatSendResult[] }>) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px]">
      {results.map((r) => {
        const accent = accentFor(r.platform);
        const tone = verdictTone(r);
        return (
          <span key={`${r.platform}:${r.account ?? ""}`} className="inline-flex items-center gap-1">
            <StatusDot tone={tone} size="sm" />
            <span className={accent.text}>{accent.label}</span>
            <span className="text-muted-foreground">
              {verdictLabel(t, r)}
              {r.detail && !r.reason ? ` — ${r.detail}` : ""}
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
  replyTo,
  onCancelReply,
}: Readonly<{
  statuses: ChatStatus[];
  limits: ChatLimit[];
  configured: boolean;
  compact?: boolean;
  /** The message being answered. While set, Send goes to that one platform as
   *  a threaded reply instead of fanning out. */
  replyTo?: ChatMessage | null;
  /** Called with the id that was sent, so a send finishing late clears only
   *  its own target; with no id (the cancel button) it clears unconditionally. */
  onCancelReply?: (id?: string) => void;
}>) {
  const t = useT();
  const [text, setText] = useState("");
  const [asBot, setAsBot] = useState(false);
  // "Send as bot" belongs to one reply. A new target, or none, starts unticked.
  useEffect(() => setAsBot(false), [replyTo?.id]);
  const [sending, setSending] = useState(false);
  const [results, setResults] = useState<ChatSendResult[] | null>(null);
  // The text of a send that only SOME platforms took. The box is cleared --
  // sending it again as-is would repeat it to the ones that accepted -- but it
  // is kept here so it can be put back and re-sent where it failed.
  const [unsent, setUnsent] = useState("");

  // WHERE THIS GOES. A message with no reply target goes, by default, only to
  // the platforms that are live: a connected chat is not a streaming one, and a
  // test line meant for one stream was once posted in two channels that were
  // not on air. `live` is the server's own reading (GET /chat/live), the same
  // one a send with no target uses, so the chips cannot disagree with it. It is
  // null until read and when the read fails; then the composer behaves as it
  // always did and lets the server decide.
  const [live, setLive] = useState<ChatLiveView | null>(null);
  const [liveFailed, setLiveFailed] = useState(false);
  // null follows the live default; a list is the operator's explicit choice.
  const [chosen, setChosen] = useState<string[] | null>(null);
  useEffect(() => {
    if (!configured || replyTo) return;
    let on = true;
    const read = () => {
      // Started inside the chain so a synchronous throw lands in .catch too.
      Promise.resolve()
        .then(() => api.chatLive())
        .then((v) => {
          if (!on) return;
          setLive(v);
          setLiveFailed(false);
        })
        .catch(() => {
          if (!on) return;
          setLive(null);
          setLiveFailed(true);
        });
    };
    read();
    const id = window.setInterval(read, 10_000);
    return () => {
      on = false;
      window.clearInterval(id);
    };
  }, [configured, replyTo, statuses]);

  const allSenders = statuses.filter((s) => s.canSend);
  // A reply goes to the one platform it answers, so only that platform's limit applies.
  // Liveness is per account, so the chips are too: one chip per connected chat
  // that can send. A platform with two accounts shows the channel on each chip.
  const connected = useMemo(
    () => allSenders.map((s) => ({ key: chatKeyOf(s.platform, s.account), status: s })),
    [allSenders],
  );
  const connectedKeys = useMemo(() => connected.map((c) => c.key), [connected]);
  const liveKeys = useMemo(() => liveChatKeys(connected, live), [connected, live]);
  // What this message will reach. Without a reading it is every sender.
  const selected = chosen ?? (live ? liveKeys : connectedKeys);
  const { noneLive, nothingChosen, showTargets, liveUnknown } = targetNotices(
    replyTo,
    live,
    liveFailed,
    connected.length,
    chosen,
  );
  // Two accounts on one platform: the chip says which channel it is.
  const perPlatform = (p: ChatPlatform) => connected.filter((c) => c.status.platform === p).length;
  const toggleTarget = (key: string) => {
    const base = chosen ?? liveKeys;
    const next = base.includes(key) ? base.filter((x) => x !== key) : [...base, key];
    const sameAsLive =
      next.length === liveKeys.length && liveKeys.every((x) => next.includes(x));
    // Back on the live set is following it again, so a go-live is picked up.
    setChosen(sameAsLive ? null : next);
  };
  const senders = replyTo
    ? allSenders.filter((s) => s.platform === replyTo.platform)
    : allSenders.filter((s) => selected.includes(chatKeyOf(s.platform, s.account)));
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

  /** The target the server is told. None when following the live default, so
   *  the server decides at the moment of sending, not at the moment of reading. */
  const sendTarget = (): ChatSendTarget | undefined => {
    if (chosen === null) return undefined;
    if (chosen.length === connectedKeys.length && connectedKeys.every((k) => chosen.includes(k))) {
      return { to: "all" };
    }
    return {
      accounts: connected
        .filter((c) => chosen.includes(c.key))
        .map((c) => ({ platform: c.status.platform, account: c.status.account ?? "" })),
    };
  };

  const sendPlain = (body: string) => {
    const to = sendTarget();
    return to ? api.sendChat(body, to) : api.sendChat(body);
  };

  const send = async () => {
    const body = text.trim();
    if (!body || sending || noneLive || nothingChosen) return;
    const target = replyTo;
    setSending(true);
    // A stale offer must not overwrite the next draft with an older message.
    setUnsent("");
    try {
      const res = replyTo
        ? await api.replyChat(
            { platform: replyTo.platform, account: replyTo.account, id: replyTo.id },
            body,
            asBot,
          )
        : await sendPlain(body);
      setResults(res.results);
      // THE DRAFT IS CLEARED ONLY WHEN SOMETHING TOOK IT (poka-yoke audit
      // 2026-11-04). It used to be cleared before the answer was read, so a
      // send no platform accepted lost the text mid-broadcast.
      // Only if the box still holds what was sent: the field stays editable
      // while the send is in flight, and a draft typed meanwhile is the
      // operator's next message, not this one.
      if (res.sent > 0) {
        // An explicit choice is for this message. The next one follows what is
        // live again, so "all" cannot linger as a surprise.
        setChosen(null);
        setText((cur) => (cur.trim() === body ? "" : cur));
        if (target) onCancelReply?.(target.id);
      }
      setUnsent(!replyTo && res.sent > 0 && res.failed > 0 ? body : "");
      const partial = partialSendText(res, t);
      if (partial !== null) toast.error(partial);
    } catch (err) {
      setResults(null);
      if (isNoPlatformLive(err)) {
        // The server's sentence is English and names JSON fields; the composer
        // says it in the operator's language and the chips are the way out.
        toast.error(t("chatpage.sendRefusedNoneLive"));
      } else {
        toast.error(err instanceof Error ? err.message : t("chatpage.couldNotSend"));
      }
    } finally {
      setSending(false);
    }
  };

  if (!configured) {
    return (
      <p className="border-t border-border px-2 py-2 text-[11px] text-muted-foreground">
        {t("chatpage.composerNotRunning")}
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
      {unsent && (
        <Button
          variant="link"
          size="sm"
          className="mb-1 h-auto p-0 text-[11px]"
          onClick={() => {
            setText(unsent);
            setUnsent("");
          }}
        >
          {t("chatpage.restoreDraft")}
        </Button>
      )}
      {replyTo && (
        <div className="mb-1.5 flex items-center gap-2 rounded-md border border-border bg-muted/40 px-2 py-1 text-[11px]">
          <span className="min-w-0 flex-1 truncate">
            {t("chatpage.replyingTo", {
              name: replyTo.author.name,
              platform: accentFor(replyTo.platform).label,
            })}
            <span className="ml-1 text-muted-foreground">{replyTo.text}</span>
          </span>
          {replyTo.platform === "kick" && (
            <label className="flex shrink-0 items-center gap-1">
              <Checkbox checked={asBot} onCheckedChange={(v) => setAsBot(v === true)} />
              {t("chatpage.replyAsBot")}
            </label>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-auto shrink-0 p-0 text-[11px]"
            onClick={() => onCancelReply?.()}
          >
            {t("chatpage.cancelReply")}
          </Button>
        </div>
      )}
      {showTargets && (
        <div className="mb-1.5 flex items-center gap-1.5 text-[11px]">
          <span className="shrink-0 text-muted-foreground">{t("chatpage.targetsLabel")}</span>
          <div className={STRIP}>
            {connected.map(({ key, status: st }) => {
              const on = selected.includes(key);
              const isLive = liveKeys.includes(key);
              const base = accentFor(st.platform).label;
              const label =
                perPlatform(st.platform) > 1 ? `${base} · ${st.channel || st.account || ""}` : base;
              const hint = t(isLive ? "chatpage.targetLive" : "chatpage.targetNotLive", {
                platform: label,
              });
              return (
                <button
                  key={key}
                  type="button"
                  aria-pressed={on}
                  title={hint}
                  aria-label={hint}
                  onClick={() => toggleTarget(key)}
                  className={cn(
                    CHIP,
                    on
                      ? "border-primary/60 bg-primary/10 text-foreground"
                      : "border-border text-muted-foreground hover:bg-card-raised hover:text-foreground",
                  )}
                >
                  <StatusDot tone={isLive ? "live" : "idle"} size="sm" />
                  {label}
                  {on && <Check className="h-3 w-3 text-primary-bright" aria-hidden />}
                </button>
              );
            })}
            <Button
              variant="link"
              size="sm"
              className="h-auto shrink-0 whitespace-nowrap p-0 px-1 text-[11px]"
              onClick={() => setChosen(connectedKeys)}
            >
              {t("chatpage.targetsAll")}
            </Button>
            {chosen !== null && (
              <Button
                variant="link"
                size="sm"
                className="h-auto shrink-0 whitespace-nowrap p-0 px-1 text-[11px]"
                onClick={() => setChosen(null)}
              >
                {t("chatpage.targetsLive")}
              </Button>
            )}
          </div>
        </div>
      )}
      {liveUnknown && (
        <output className="block text-pretty mb-1.5 text-[11px] text-muted-foreground">
          {t("chatpage.liveUnknown")}
        </output>
      )}
      {(noneLive || nothingChosen) && (
        <output className="block text-pretty mb-1.5 text-[11px] text-warn">
          {t(noneLive ? "chatpage.targetsNoneLive" : "chatpage.targetsPick")}
        </output>
      )}
      <div className="flex items-end gap-1.5">
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter breaks the line. The other way round
            // makes a chat box feel like a form.
            // An IME confirms a candidate with an Enter that belongs to the
            // composition, not to the message. isComposing is the standard
            // signal; Safari reports the same keystroke as keyCode 229.
            if (e.nativeEvent.isComposing || e.keyCode === 229) return; // NOSONAR S1874: Safari reports the IME-confirming Enter only as keyCode 229; key and code say "Enter"
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
          rows={compact ? 1 : 2}
          placeholder={
            composerPlaceholder(t, replyTo, allSenders, senders, live)
          }
          className="min-h-0 resize-none text-[12px]"
          aria-label={
            replyTo
              ? t("chatpage.replyPlaceholder", { name: replyTo.author.name })
              : t("chatpage.composerAria")
          }
        />
        {allSenders.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("chatpage.noSender")}</p>
        )}
        {/* Compact (the Dashboard dock) drops the word, so the glyph needs a
            name: it was the one unnamed button on the Dashboard. */}
        <Button
          size="sm"
          onClick={() => void send()}
          disabled={sending || !text.trim() || noneLive || nothingChosen}
          aria-label={compact ? t("chatpage.send") : undefined}
          title={compact ? t("chatpage.send") : undefined}
        >
          {sending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Send className="h-3 w-3" />}
          {!compact && t("chatpage.send")}
        </Button>
      </div>
      {strictest > 0 && (
        <p
          className={cn(
            "mt-1 text-sm",
            over ? "text-warn" : "text-muted-foreground",
          )}
        >
          <span className="tnum font-mono">
            {[...text].length}/{strictest}
          </span>{" "}
          {limitHint(t, over, strictestPlatform, strictest)}
        </p>
      )}
    </div>
  );
}

// --------------------------------------------------------------- status rail

/** "12 watching · 3,400 followers · 210 subscribers", with whichever figures the
 *  platform reported; empty when it reported none. */
function audienceLine(s: ChatStatus, t: ReturnType<typeof useT>): string {
  const parts: string[] = [];
  if (s.viewers !== undefined) {
    parts.push(t("chatpage.countViewers", { n: s.viewers.toLocaleString() }));
  }
  if (s.followers !== undefined) {
    parts.push(t("chatpage.countFollowers", { n: s.followers.toLocaleString() }));
  }
  if (s.subscribers !== undefined) {
    parts.push(t("chatpage.countSubscribers", { n: s.subscribers.toLocaleString() }));
  }
  return parts.join(" · ");
}

/** Per-platform connection state, with the reason attached.
 *
 *  The whole point of this block is the sentence. "YouTube: quota exhausted,
 *  resumes 00:00" is actionable; a red dot is a puzzle. */
export function ChatStatusList({
  statuses,
  compact,
}: Readonly<{
  statuses: ChatStatus[];
  compact?: boolean;
}>) {
  const t = useT();
  const tn = useTN();
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
                <span className="ml-auto text-sm uppercase tracking-wider text-muted-foreground">
                  {CHAT_STATE_KEY[s.state] ? t(CHAT_STATE_KEY[s.state]) : s.state}
                </span>
              </div>
              {detail && (
                <p className="mt-0.5 text-[11px] leading-snug text-muted-foreground">{detail}</p>
              )}
              {audienceLine(s, t) && (
                <p className="mt-0.5 text-sm leading-snug text-muted-foreground">
                  {audienceLine(s, t)}
                </p>
              )}
              {quota && (
                <p className="mt-0.5 text-sm leading-snug text-muted-foreground">
                  {tn("chatpage.quotaUsed", {
                    n: (
                      <span className="tnum font-mono">
                        {quota.used.toLocaleString()}/{quota.limit.toLocaleString()}
                      </span>
                    ),
                  })}
                  {quota.resetAt
                    ? t("chatpage.quotaResets", { time: clockTime(quota.resetAt) })
                    : ""}
                  {quota.paused ? t("chatpage.quotaPaused") : ""}
                  {quota.intervalMs > 0
                    ? t("chatpage.quotaPolling", { seconds: Math.round(quota.intervalMs / 1000) })
                    : ""}
                </p>
              )}
              {!compact && (
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {tn(s.received === 1 ? "chatpage.receivedCountOne" : "chatpage.receivedCount", {
                    n: <span className="tnum font-mono">{s.received.toLocaleString()}</span>,
                  })}
                  {" · "}
                  {tn(s.sent === 1 ? "chatpage.sentCountOne" : "chatpage.sentCount", {
                    n: <span className="tnum font-mono">{s.sent.toLocaleString()}</span>,
                  })}
                  {s.restarts > 0 && (
                    <>
                      {" · "}
                      {tn(s.restarts === 1 ? "chatpage.reconnectOne" : "chatpage.reconnectMany", {
                        n: <span className="tnum font-mono">{s.restarts}</span>,
                      })}
                    </>
                  )}
                  {!s.canSend && ` · ${t("chatpage.receiveOnly")}`}
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
}: Readonly<{
  search: ChatSearchState;
  className?: string;
}>) {
  const t = useT();
  return (
    <div
      className={cn(
        "flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1",
        className,
      )}
    >
      <Search className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
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
        placeholder={t("chatpage.searchPlaceholder")}
        aria-label={t("chatpage.searchAria")}
        className="min-w-0 flex-1 bg-transparent text-[12px] outline-none placeholder:text-subtle-foreground"
      />
      {search.loading && <Loader2 className="h-3 w-3 shrink-0 animate-spin text-muted-foreground" />}
      {search.query && (
        <button
          type="button"
          onClick={search.clear}
          aria-label={t("lib.clearSearch")}
          className="shrink-0 rounded p-0.5 text-muted-foreground hover:text-foreground"
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
}: Readonly<{
  search: ChatSearchState;
  onDelete?: Asks<ChatMessage>;
  onOpenUser?: (m: ChatMessage) => void;
  onMenu?: (m: ChatMessage, at: { x: number; y: number }) => void;
}>) {
  const t = useT();
  const searchArea = searchAreaFor(search.error, search.results.length, search.loading);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 bg-card-raised px-2 py-1 text-sm text-muted-foreground">
        <span>
          {matchLabel(t, search.loading, search.results.length)}
          {search.truncated && ` ${t("chatpage.showingNewest")}`}
        </span>
        <span className="ml-auto">{t("chatpage.newestFirst")}</span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto py-1" role="log">
        {searchArea === "error" && (
          <p className="px-2 py-4 text-[12px] text-down">{search.error}</p>
        )}
        {searchArea === "empty" && (
          // The caveat matters most exactly here. An empty result is the one
          // outcome an operator can misread as evidence, and "no matches" on
          // its own invites "then they never said it" — which is a conclusion
          // about a purged table, not about a person.
          <div className="px-2 py-4 text-[12px] text-muted-foreground">
            <p>{t("chatpage.noMatchesInScrollback")}</p>
            {search.note && (
              <p className="mt-1 text-[11px] text-muted-foreground">{search.note}</p>
            )}
          </div>
        )}
        {searchArea === "results" &&
          search.results.map((m) => (
            <MessageRow
              key={messageKey(m)}
              m={m}
              onDelete={onDelete}
              onOpenUser={onOpenUser}
              onMenu={onMenu}
            />
          ))}
      </div>

      {search.results.length > 0 && search.note && (
        <p className="shrink-0 border-t border-border px-2 py-1 text-sm leading-snug text-muted-foreground">
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
}: Readonly<{
  className?: string;
  showComposer?: boolean;
}>) {
  const t = useT();
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
  const attached = useMemo(() => new Set(statuses.map((s) => s.platform)), [statuses]);

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
  const [replyTo, setReplyTo] = useState<ChatMessage | null>(null);
  const search = useChatSearch();

  // THE SECOND SURFACE, and the reason this is here rather than only on
  // ChatPage. #770 was filed against the chat page's menu and fixed there; this
  // panel is the same menu mounted in the dashboard pane, reached by the same
  // right-click reflex, and it still deleted on one click. A confirmation that
  // exists on one of two identical surfaces is the inconsistency the issue was
  // about, one level down.
  const confirmDelete = useConfirm<ChatMessage>();

  const del = useCallback(
    async (m: ChatMessage) => {
      try {
        await remove(m);
      } catch (err) {
        // The platform's own words — "polyemesis cannot delete twitch
        // messages; use the twitch dashboard" is the whole answer.
        toast.error(err instanceof Error ? err.message : t("chatpage.couldNotDeleteThatMessage"));
      }
    },
    [remove, t],
  );

  return (
    <div className={cn("@container flex min-h-0 flex-col rounded-md border border-border bg-card", className)}>
      <ConfirmDeleteMessage
        target={confirmDelete.target}
        onOpenChange={confirmDelete.onOpenChange}
        onConfirm={del}
      />
      {card && (
        <ChatUserCard
          platform={card.platform}
          account={card.account}
          authorId={card.author.id ?? ""}
          authorName={card.author.name}
          channel={card.channel}
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
          onDelete={confirmDelete.ask}
          onReply={showComposer ? setReplyTo : undefined}
        />
      )}
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-2 py-1">
        <div className="flex min-w-0 shrink items-center gap-1.5">
          <MessagesSquare className="hidden h-3.5 w-3.5 shrink-0 text-muted-foreground @sm:block" />
          <span className="shrink-0 text-[12px] font-semibold">{t("chatpage.title")}</span>
          {stored && (
            <Badge variant="outline" className="shrink-0" title={t("chatpage.panelStoredTitle")}>
              {t("chatpage.historyBadge")}
            </Badge>
          )}
          {configured && !connected && (
            <Badge variant="warn" className="shrink-0" title={t("chatpage.socketClosedTitle")}>
              {t("chrome.socketOffline")}
            </Badge>
          )}
          {/* #13/#21: unlike a closed socket, a frame that failed to parse gives
              no other signal at all -- `connected` still reads true. */}
          {frameError && (
            <Badge
              variant="warn"
              className="shrink-0"
              title={t("i18nSweep.chatFrameErrorHint")}
            >
              {t("i18nSweep.frameError")}
            </Badge>
          )}
        </div>
        <PlatformFilter
          className="ml-auto"
          platforms={platforms}
          hidden={hidden}
          onToggle={toggle}
          attached={attached}
        />
      </div>

      <ChatSearchBox search={search} />

      {search.active ? (
        <ChatSearchResults
          search={search}
          onDelete={confirmDelete.ask}
          onOpenUser={setCard}
          onMenu={(m, at) => setMenu({ m, at })}
        />
      ) : (
        <ChatTimeline
          messages={visible}
          onDelete={confirmDelete.ask}
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
        <ChatComposer
          statuses={statuses}
          limits={limits}
          configured={configured}
          compact
          replyTo={replyTo}
          onCancelReply={(id) => setReplyTo((cur) => (id === undefined || cur?.id === id ? null : cur))}
        />
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
  onOpenCredentials,
}: Readonly<{
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
  /** Where "no account connected" sends the operator: the kit's primary
   *  action on that empty state. Optional, because the Dashboard's chat card
   *  already sits beside its own connect prompt. */
  onOpenCredentials?: () => void;
}>) {
  const t = useT();
  // Loading is not empty: it is a wait, so it keeps the spinner and no title.
  if (loading) {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center">
        <p className="max-w-sm text-sm leading-relaxed text-muted-foreground">
          <Loader2 className="mr-1 inline h-3 w-3 animate-spin" />
          {t("chatpage.emptyLoading")}
        </p>
      </div>
    );
  }
  // Every other situation is the kit's EmptyState: a title naming what is
  // missing, the existing sentence as its body (it was an 11px line, below
  // the 12px floor). Same ranking as before.
  let title: TranslationKey;
  let body: string;
  if (error) {
    title = "chatpage.emptyErrorTitle";
    body = error;
  } else if (!configured) {
    title = "chatpage.emptyOffTitle";
    body = t("chatpage.emptyNotRunning");
  } else if (statuses.length === 0) {
    title = "chatpage.emptyNoAccountTitle";
    body = t("chatpage.emptyNoAccount");
  } else if (hiddenMessages > 0) {
    // Ranked ABOVE "nothing has been said": when the filter is the reason the
    // pane is empty, it is the only reason worth printing.
    title = "chatpage.emptyFilteredTitle";
    body =
      hiddenMessages === 1
        ? t("chatpage.emptyFilteredOne")
        : t("chatpage.emptyFilteredMany", { count: hiddenMessages });
  } else {
    title = "chatpage.emptyWaitingTitle";
    body = t("chatpage.emptyWaiting");
  }

  // The one empty state with a next step on another page gets it as the
  // kit's primary action, not only as a link in the side note.
  const action =
    title === "chatpage.emptyNoAccountTitle" && onOpenCredentials
      ? { label: t("chatpage.openCredentials"), onClick: onOpenCredentials }
      : undefined;
  return (
    <div className="flex h-full items-center justify-center p-3">
      <EmptyState icon={MessagesSquare} title={t(title)} body={body} action={action} />
    </div>
  );
}

// The field says the state in two words. The reason is text under
// it, where it can wrap; inside a placeholder it was clipped mid-sentence (A88).
function composerPlaceholder(
  t: Translator,
  replyTo: { author: { name: string } } | null | undefined,
  allSenders: readonly unknown[],
  senders: ReadonlyArray<{ platform: Parameters<typeof accentFor>[0] }>,
  live: unknown,
): string {
  if (replyTo) return t("chatpage.replyPlaceholder", { name: replyTo.author.name });
  if (allSenders.length === 0) return t("chatpage.notConnected");
  if (senders.length === 0) return t("chatpage.targetsPick");
  if (!live) return t("chatpage.sendLiveDefault");
  return t("chatpage.sendTo", { platforms: senders.map((s) => accentFor(s.platform).label).join(", ") });
}

// One key per connected chat: a platform with two accounts has two keys.
const chatKeyOf = (platform: ChatPlatform, account?: string) => `${platform}\u0000${account ?? ""}`;

// The connected chats the live reading marks as able to send and live right now.
function liveChatKeys<T extends { key: string }>(connected: readonly T[], live: ChatLiveView | null): string[] {
  return connected
    .filter((c) => live?.targets.some((x) => chatKeyOf(x.platform, x.account) === c.key && x.canSend && x.live))
    .map((c) => c.key);
}

// Which target notices the composer shows, from the reply being answered, the
// live reading, and the chats the user has picked (null means "follow live").
function targetNotices(
  replyTo: unknown,
  live: ChatLiveView | null,
  liveFailed: boolean,
  connectedCount: number,
  chosen: readonly string[] | null,
) {
  return {
    noneLive: !replyTo && !!live && chosen === null && !live.anyLive,
    nothingChosen: !replyTo && chosen !== null && chosen.length === 0,
    showTargets: !replyTo && !!live && connectedCount > 0,
    liveUnknown: !replyTo && liveFailed && connectedCount > 0,
  };
}

// The warning for a send that some platforms refused: null when every one went out.
function partialSendText(res: { sent: number; failed: number }, t: Translator): string | null {
  if (res.failed <= 0) return null;
  return res.sent > 0
    ? t("chatpage.sentPartial", { sent: res.sent, failed: res.failed })
    : t("chatpage.noneAccepted");
}

// The server refuses a send with this code when no platform is live.
function isNoPlatformLive(err: unknown): boolean {
  return err instanceof ApiError && err.code === "no_platform_live";
}
