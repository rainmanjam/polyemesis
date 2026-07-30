import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";
import { MessagesSquare, RefreshCw, WifiOff } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/AppLayout";
import {
  ChatComposer,
  ChatEmpty,
  ChatStatusList,
  ChatTimeline,
  PlatformFilter,
} from "@/components/ChatPanel";
import { accentFor, platformsIn } from "@/lib/chat";
import { ChatUserCard } from "@/components/ChatUserCard";
import { ChatRules } from "@/components/ChatRules";
import { useChatFeed } from "@/hooks/useChatFeed";
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
    reload,
    remove,
  } = useChatFeed();

  const [hidden, setHidden] = useState<Set<ChatPlatform>>(new Set());

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

  const del = useCallback(
    async (m: ChatMessage) => {
      try {
        await remove(m);
        toast.success(`Deleted on ${accentFor(m.platform).label}.`);
      } catch (err) {
        // Verbatim. The server's sentence names the platform and, where one
        // exists, where the moderator can do it instead; rewording it here
        // would only make it vaguer.
        toast.error(err instanceof Error ? err.message : "Could not delete that message.");
      }
    },
    [remove],
  );

  // A platform is worth calling out at the top when it is neither live nor
  // deliberately stopped: those are the two states nobody needs telling about.
  const troubled = statuses.filter((s) => s.state === "degraded" || s.state === "failed");

  return (
    <div className="flex h-full min-h-0 flex-col p-3">
      <PageHeader
        title="Chat"
        subtitle="Every platform in one timeline, with one send box."
        actions={
          <>
            {stored && (
              <Badge variant="outline" title="Nothing is attached; this is the stored scrollback.">
                stored history
              </Badge>
            )}
            {configured && !connected && (
              <Badge variant="warn">
                <WifiOff className="h-3 w-3" />
                socket offline
              </Badge>
            )}
            <Button variant="ghost" size="icon-sm" onClick={() => void reload()} aria-label="Reload">
              <RefreshCw className={loading ? "animate-spin" : undefined} />
            </Button>
          </>
        }
      />

      <div className="grid min-h-0 flex-1 gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* ---- the merged timeline ---- */}
        <section className="flex min-h-0 flex-col rounded-md border border-border bg-card">
          <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-2 py-1.5">
            <MessagesSquare className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-[12px] font-semibold">Timeline</span>
            <span className="tnum font-mono text-[10px] text-subtle-foreground">
              {visible.length}
              {visible.length !== messages.length && ` of ${messages.length}`}
            </span>
            <div className="ml-auto">
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

          <ChatTimeline
            messages={visible}
            onDelete={del}
            onOpenUser={setCard}
            empty={
              hidden.size > 0 && messages.length > 0 ? (
                <div className="flex h-full items-center justify-center px-6 text-center">
                  <p className="text-[11px] text-muted-foreground">
                    Every platform with messages is filtered out.
                  </p>
                </div>
              ) : (
                <ChatEmpty
                  loading={loading}
                  configured={configured}
                  error={error}
                  statuses={statuses}
                />
              )
            }
          />

          <ChatComposer statuses={statuses} limits={limits} configured={configured} />
        </section>

        {/* ---- connections ---- */}
        <aside className="flex min-h-0 flex-col gap-2 overflow-y-auto">
          {troubled.length > 0 && (
            <div className="rounded-md border border-warn/40 bg-warn-dim px-2 py-1.5">
              <p className="text-[11px] font-semibold text-warn">
                {troubled.length} platform{troubled.length === 1 ? "" : "s"} need
                {troubled.length === 1 ? "s" : ""} attention
              </p>
            </div>
          )}

          <ChatRules statuses={statuses} />
          <ChatStatusList statuses={statuses} />

          {configured && statuses.length === 0 && (
            <p className="rounded-md border border-border bg-card px-2 py-1.5 text-[11px] leading-relaxed text-muted-foreground">
              Chat is running, but no platform account is attached. Connect one under Settings →
              Platform credentials.
            </p>
          )}

          {!configured && !loading && (
            <p className="rounded-md border border-border bg-card px-2 py-1.5 text-[11px] leading-relaxed text-muted-foreground">
              No chat is running on this server. The scrollback below the timeline is whatever was
              stored before, and the send box is disabled until a platform is attached.
            </p>
          )}

          {stats && (
            <dl className="rounded-md border border-border bg-card px-2 py-1.5 text-[10px] text-subtle-foreground">
              <div className="flex justify-between gap-2">
                <dt>Received</dt>
                <dd className="tnum font-mono">{stats.received.toLocaleString()}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt title="The same message seen twice — our own echo, or a platform replaying it.">
                  Deduplicated
                </dt>
                <dd className="tnum font-mono">{stats.deduped.toLocaleString()}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt>Stored</dt>
                <dd className="tnum font-mono">{stats.stored.toLocaleString()}</dd>
              </div>
              {stats.dropped > 0 && (
                <div className="flex justify-between gap-2 text-warn">
                  <dt title="Shown live, but persistence fell behind and the scrollback lost them.">
                    Dropped from history
                  </dt>
                  <dd className="tnum font-mono">{stats.dropped.toLocaleString()}</dd>
                </div>
              )}
            </dl>
          )}

          <p className="px-1 text-[10px] leading-relaxed text-subtle-foreground">
            Deleting removes the message on the platform that issued it. A platform polyemesis
            cannot delete on says so and names where it can be done instead — the action is never
            hidden on a guess.
          </p>
        </aside>
      </div>
    </div>
  );
}
