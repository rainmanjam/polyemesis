import { useCallback, useMemo, useState } from "react";
import { settingsHref } from "@/lib/settingsSearch";
import { toast } from "sonner";
import { Flag, MessagesSquare, RefreshCw, WifiOff } from "lucide-react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/AppLayout";
import { ChatExportDialog } from "@/components/ChatExportDialog";
import { EmptyState } from "@/components/EmptyState";
import {
  ChatComposer,
  ChatEmpty,
  ChatSearchBox,
  ChatSearchResults,
  ChatStatusList,
  ChatTimeline,
  PlatformFilter,
} from "@/components/ChatPanel";
import { accentFor, platformsIn } from "@/lib/chat";
import { ChatUserCard } from "@/components/ChatUserCard";
import { ChatFlaggedList } from "@/components/ChatFlaggedList";
import { ChatMessageMenu, type MenuAnchor } from "@/components/ChatMessageMenu";
import { ChatRules } from "@/components/ChatRules";
import { XChatModeration } from "@/components/XChatModeration";
import { ChatModerators } from "@/components/ChatModerators";
import { TwitchTools } from "@/components/TwitchTools";
import { PollPanel } from "@/components/PollPanel";
import { ConfirmDeleteMessage } from "@/components/ConfirmDeleteMessage";
import { useChatFeed } from "@/hooks/useChatFeed";
import { useChatSearch } from "@/hooks/useChatSearch";
import { ActivityTab } from "@/features/activity/ActivityTab";
import { useChatFlags } from "@/features/chat/useChatFlags";
import { useConfirm } from "@/hooks/useConfirm";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { mayClaim, readFailed } from "@/lib/readState";
import type { ChatMessage, ChatPlatform } from "@/lib/types";

/** One pane for every platform at once.
 *
 *  The merged timeline is the feature: a broadcaster reading four browser tabs
 *  is a broadcaster who misses three of them. Everything on this page follows
 *  from that — the accent that says where a line came from without a label, the
 *  one send box that fans out, and the per-platform verdict that admits when
 *  half the fan-out failed.
 *
 *  The panel, the timeline and the composer all live in ChatPanel.tsx and are
 *  shared with the compact dashboard pane. This file is layout. */
export function ChatPage() {
  const t = useT();
  const navigate = useNavigate();
  // Chat | Activity. The tab lives in the URL so a reload or a shared link
  // lands on the same one; Chat, the default, adds nothing to it.
  const [params, setParams] = useSearchParams();
  const tab: "chat" | "activity" = params.get("tab") === "activity" ? "activity" : "chat";
  const pickTab = (next: "chat" | "activity") =>
    setParams(
      (p) => {
        const q = new URLSearchParams(p);
        if (next === "chat") q.delete("tab");
        else q.set("tab", next);
        return q;
      },
      { replace: true },
    );
  const {
    messages,
    statuses,
    limits,
    stats,
    configured,
    connected,
    loading,
    stored,
    error,
    frameError,
    reload,
    remove,
  } = useChatFeed();

  const [hidden, setHidden] = useState<Set<ChatPlatform>>(new Set());
  const [menu, setMenu] = useState<{ m: ChatMessage; at: MenuAnchor } | null>(null);
  const [replyTo, setReplyTo] = useState<ChatMessage | null>(null);
  const search = useChatSearch();

  // UX Council A48: what automod flagged, behind a chip with its count. The
  // list honours the same platform filter as the timeline, so a filtered-out
  // platform's flags are not counted either.
  const [showFlags, setShowFlags] = useState(false);
  // Search wins: typing in the search box while the list is open shows the
  // results, rather than firing a search nobody can see (#945 review).
  const flagsOpen = showFlags && !search.active;
  const flagsRead = useChatFlags(flagsOpen);
  const reloadFlags = flagsRead.reload;
  const allFlags = useMemo(
    () => (mayClaim(flagsRead.read) ? flagsRead.read.value.flags : []),
    [flagsRead.read],
  );
  const flags = useMemo(
    () => allFlags.filter((f) => !hidden.has(f.message.platform)),
    [allFlags, hidden],
  );
  // From the server's per-platform counts, narrowed to the platforms shown.
  // Exact over the whole held list, so there is no "+": a "+" from the
  // unfiltered list once read "Flagged 0+" on a filtered view.
  const flagCount = mayClaim(flagsRead.read)
    ? String(
        Object.entries(flagsRead.read.value.counts ?? {}).reduce(
          (n, [p, c]) => (hidden.has(p as ChatPlatform) ? n : n + (c ?? 0)),
          0,
        ),
      )
    : "";
  const toggleFlags = () => {
    if (flagsOpen) {
      setShowFlags(false);
      return;
    }
    // Opening the list clears a search that would otherwise keep it hidden.
    if (search.active) search.setQuery("");
    setShowFlags(true);
  };

  const platforms = useMemo(() => platformsIn(messages, statuses), [messages, statuses]);
  const counts = useMemo(() => {
    const m = new Map<ChatPlatform, number>();
    messages.forEach((msg) => m.set(msg.platform, (m.get(msg.platform) ?? 0) + 1));
    return m;
  }, [messages]);
  const visible = useMemo(
    () => messages.filter((m) => !hidden.has(m.platform)),
    [messages, hidden],
  );

  const toggle = (p: ChatPlatform) =>
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  // The message whose author's card is open. The whole message, not an id: the
  // card needs the platform and account to address a moderation call, and
  // cannot re-derive either.
  const [card, setCard] = useState<ChatMessage | null>(null);

  /* DELETING ASKS FIRST, like every other destructive action in this app.
   *
   * It did not. The trash icon on a row, the Delete item in the right-click
   * menu and the same item over a search result all called this straight
   * through to the platform — while a permanent ban, two items above it in
   * that same menu, deliberately refuses to be one click and routes through
   * the user card to be confirmed. Two irreversible actions a centimetre
   * apart, one guarded and one not, is worse than neither being guarded: it
   * trains the caution out of an operator exactly where it matters. That
   * inconsistency is the defect ConfirmDestructive's own header names as the
   * reason it exists.
   *
   * It is also the delete with the smallest target in the product. The icon
   * appears on hover, at the right-hand edge of a row, in a list that moves on
   * its own while a stream is live — the pointer is over one message when the
   * decision is made and over the next one when the button arrives. A
   * mis-click removed a viewer's message on the platform with no undo and no
   * way to find out which one it had been.
   *
   * ONE dialog for all three entry points, because a per-caller confirmation
   * is how one of them ends up without one — the point useConfirm's own header
   * makes about three pages rolling their own `deleting` state.
   */
  const confirmDelete = useConfirm<ChatMessage>();

  const release = useCallback(
    async (m: ChatMessage) => {
      try {
        await api.releaseChatFlag({ platform: m.platform, account: m.account, id: m.id });
        toast.success(t("chatFlags.released"));
      } catch (err) {
        // The server says which of the three reasons it was.
        toast.error(err instanceof Error ? err.message : t("chatFlags.releaseFailed"));
      } finally {
        // Either way the list is stale: a released line is no longer held, and a
        // refused one was not (or no longer is).
        reloadFlags();
      }
    },
    [t, reloadFlags],
  );

  const del = useCallback(
    async (m: ChatMessage) => {
      try {
        await remove(m);
        toast.success(t("chatpage.deletedOn", { platform: accentFor(m.platform).label }));
      } catch (err) {
        // Verbatim. The server's sentence names the platform and, where one
        // exists, where the moderator can do it instead; rewording it here
        // would only make it vaguer.
        toast.error(err instanceof Error ? err.message : t("chatpage.couldNotDeleteThatMessage"));
      }
    },
    [remove, t],
  );

  // A platform is worth calling out at the top when it is neither live nor
  // deliberately stopped: those are the two states nobody needs telling about.
  const troubled = statuses.filter((s) => s.state === "degraded" || s.state === "failed");

  const renderTimeline = () => {
    if (flagsOpen) {
      return (
        <ChatFlaggedList
          flags={flags}
          failed={readFailed(flagsRead.read)}
          filtered={flags.length === 0 && allFlags.length > 0}
          onOpenUser={setCard}
          onRelease={release}
        />
      );
    }
    if (search.active) {
      return (
        <ChatSearchResults
          search={search}
          onDelete={confirmDelete.ask}
          onOpenUser={setCard}
          onMenu={(m, at) => setMenu({ m, at })}
        />
      );
    }
    return (
      <ChatTimeline
        messages={visible}
        onDelete={confirmDelete.ask}
        onOpenUser={setCard}
        onMenu={(m, at) => setMenu({ m, at })}
        empty={
          hidden.size > 0 && messages.length > 0 ? (
            <div className="flex h-full items-center justify-center p-3">
              <EmptyState
                icon={MessagesSquare}
                title={t("chatpage.emptyFilteredTitle")}
                body={t("chatpage.allFiltered")}
              />
            </div>
          ) : (
            <ChatEmpty
              loading={loading}
              configured={configured}
              error={error}
              statuses={statuses}
              onOpenCredentials={() => void navigate(settingsHref("platforms"))}
            />
          )
        }
      />
    );
  };

  return (
    <div className="flex h-full min-h-0 flex-col p-3">
      <PageHeader
        title={t("chatpage.title")}
        subtitle={t("chatpage.subtitle")}
        actions={
          <>
            {stored && (
              <Badge variant="outline" title={t("chatpage.storedTitle")}>
                {t("chatpage.storedHistory")}
              </Badge>
            )}
            {configured && !connected && (
              <>
                <Badge variant="warn" title={t("chrome.socketOfflineHint")}>
                  <WifiOff className="h-3 w-3" />
                  {t("chatpage.socketOffline")}
                </Badge>
                {/* The reason sits beside the badge, not only in its tooltip:
                    a tooltip cannot be read by a keyboard or touch user. */}
                <span className="text-sm text-muted-foreground">{t("chrome.socketOfflineHint")}</span>
              </>
            )}
            {/* #13/#21: `connected` cannot say this -- the socket stays open
                while a frame it delivered fails to parse and is dropped. */}
            {frameError && (
              <>
                <Badge variant="warn" title={t("i18nSweep.chatPageFrameErrorHint")}>
                  {t("i18nSweep.frameError")}
                </Badge>
                <span className="text-sm text-muted-foreground">
                  {t("i18nSweep.chatPageFrameErrorHint")}
                </span>
              </>
            )}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={() => {
                void reload();
                flagsRead.reload();
              }}
              aria-label={t("chatpage.reload")}
            >
              <RefreshCw className={loading ? "animate-spin" : undefined} />
            </Button>
          </>
        }
      />

      <div role="tablist" aria-label={t("activity.tabsLabel")} className="mb-2 flex shrink-0 gap-1">
        {(["chat", "activity"] as const).map((id) => (
          <Button
            key={id}
            role="tab"
            id={`chat-tab-${id}`}
            aria-selected={tab === id}
            aria-controls={`chat-panel-${id}`}
            size="sm"
            variant={tab === id ? "secondary" : "ghost"}
            onClick={() => pickTab(id)}
          >
            {t(id === "chat" ? "activity.tab.chat" : "activity.tab.activity")}
          </Button>
        ))}
      </div>

      {tab === "activity" && (
        <div role="tabpanel" id="chat-panel-activity" aria-labelledby="chat-tab-activity" className="flex min-h-0 flex-1 flex-col">
          <ActivityTab />
        </div>
      )}

      <div
        role="tabpanel"
        id="chat-panel-chat"
        aria-labelledby="chat-tab-chat"
        hidden={tab !== "chat"}
        className={tab === "chat" ? "grid min-h-0 flex-1 gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]" : "hidden"}
      >
        {/* ---- the merged timeline ---- */}
        <section className="flex min-h-0 flex-col rounded-md border border-border bg-card">
          <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-2 py-1.5">
            <MessagesSquare className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-sm font-semibold">{t("chatpage.timeline")}</span>
            <span className="tnum font-mono text-sm text-muted-foreground">
              {visible.length !== messages.length
                ? t("chatpage.shownOfTotal", { shown: visible.length, total: messages.length })
                : visible.length}
            </span>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              {/* A toggle, so aria-pressed says which list is on screen. The
                  count rides in the accessible name too: a number beside an
                  icon is otherwise announced as a bare digit. */}
              <Button
                size="sm"
                variant={flagsOpen ? "secondary" : "outline"}
                aria-pressed={flagsOpen}
                aria-label={t("chatFlags.chipAria", { count: flagCount || "0" })}
                onClick={toggleFlags}
              >
                <Flag aria-hidden="true" className="mr-1 h-3 w-3" />
                {t("chatFlags.chip")}
                {flagCount !== "" && <span className="tnum ml-1 font-mono">{flagCount}</span>}
              </Button>
              <ChatExportDialog platforms={platforms} />
              <PlatformFilter
                platforms={platforms}
                hidden={hidden}
                onToggle={toggle}
                counts={counts}
              />
            </div>
          </div>

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
              onDelete={confirmDelete.ask}
              onReply={setReplyTo}
            />
          )}

          {/* The one confirmation, shared by the row icon, the right-click menu
              and the search results. It names the platform and QUOTES THE
              MESSAGE, which is the part that actually guards against the
              mis-click this exists for: the operator can see they are about to
              remove the line they meant, not the one that scrolled into its
              place.

              No requireTyping. That is reserved for things whose name is worth
              typing — a recording, a credential, a cascade — and a chat
              message has no name to type. It also has to stay usable at the
              speed of a live chat: the permanent ban next to it is one
              confirming click, and matching that keeps the friction
              proportional rather than making this the heaviest control in the
              pane. */}
          <ConfirmDeleteMessage
            target={confirmDelete.target}
            onOpenChange={confirmDelete.onOpenChange}
            onConfirm={del}
          />

          <ChatSearchBox search={search} />

          {renderTimeline()}

          <ChatComposer
            statuses={statuses}
            limits={limits}
            configured={configured}
            replyTo={replyTo}
            onCancelReply={() => setReplyTo(null)}
          />
        </section>

        {/* ---- connections ---- */}
        <aside className="flex min-h-0 flex-col gap-2 overflow-y-auto">
          {troubled.length > 0 && (
            <div className="rounded-md border border-warn/40 bg-warn-dim px-2 py-1.5">
              <p className="text-sm font-semibold text-warn">
                {t(troubled.length === 1 ? "chatpage.attentionOne" : "chatpage.attentionMany", {
                  count: troubled.length,
                })}
              </p>
              {/* Which platform, and why, in the banner itself. A count alone sends
                  the moderator to scan every card to find the one that is failing. */}
              <ul className="mt-0.5 flex flex-col gap-0.5">
                {troubled.map((s) => {
                  const label = accentFor(s.platform).label;
                  const reason = s.detail || s.lastError;
                  return (
                    <li key={`${s.platform}-${s.account ?? ""}`} className="text-sm text-warn">
                      {reason ? t("chatpage.attentionItem", { platform: label, reason }) : label}
                    </li>
                  );
                })}
              </ul>
            </div>
          )}

          {/* The connection status is the first thing in the sidebar: it answers
              "is it working", which is what a moderator needs before the poll
              form, and before the rules. */}
          <ChatStatusList statuses={statuses} />
          <ChatRules statuses={statuses} />
          <XChatModeration statuses={statuses} />
          <ChatModerators statuses={statuses} />
          <TwitchTools statuses={statuses} />
          <PollPanel twitchAvailable={statuses.some((s) => s.platform === "twitch")} />

          {configured && statuses.length === 0 && (
            <p className="rounded-md border border-border bg-card px-2 py-1.5 text-sm leading-relaxed text-muted-foreground">
              {t("chatpage.noAccount")}{" "}
              <Link to={settingsHref("platforms")} className="text-primary-bright underline-offset-2 hover:underline">
                {t("chatpage.openCredentials")}
              </Link>
            </p>
          )}

          {!configured && !loading && (
            <p className="rounded-md border border-border bg-card px-2 py-1.5 text-sm leading-relaxed text-muted-foreground">
                    {t("chatpage.notRunning")}
            </p>
          )}

          {stats && (
            <dl className="rounded-md border border-border bg-card px-2 py-1.5 text-sm text-muted-foreground">
              <div className="flex justify-between gap-2">
                <dt>{t("chatpage.received")}</dt>
                <dd className="tnum font-mono">{stats.received.toLocaleString()}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt title={t("chatpage.dupTitle")}>
                  {t("chatpage.deduplicated")}
                </dt>
                <dd className="tnum font-mono">{stats.deduped.toLocaleString()}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt>{t("chatpage.stored")}</dt>
                <dd className="tnum font-mono">{stats.stored.toLocaleString()}</dd>
              </div>
              {stats.dropped > 0 && (
                <div className="flex justify-between gap-2 text-warn">
                  <dt title={t("chatpage.droppedTitle")}>
                    {t("chatpage.droppedFromHistory")}
                  </dt>
                  <dd className="tnum font-mono">{stats.dropped.toLocaleString()}</dd>
                </div>
              )}
            </dl>
          )}

          <p className="px-1 text-sm leading-relaxed text-muted-foreground">
                    {t("chatpage.deleteNote")}
          </p>
        </aside>
      </div>
    </div>
  );
}
