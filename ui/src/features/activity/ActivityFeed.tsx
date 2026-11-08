import { useEffect, useMemo, useState, type ComponentType } from "react";
import { toast } from "sonner";
import {
  BarChart3,
  Coins,
  Flame,
  Gift,
  Megaphone,
  Radio,
  Repeat,
  Repeat2,
  ShieldAlert,
  Star,
  Swords,
  Target,
  Ticket,
  ThumbsUp,
  UserPlus,
  Activity as ActivityIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/EmptyState";
import { accentFor } from "@/lib/chat";
import { dayAwareTime, timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import type { ActivityEvent, ActivityKind, ChatPlatform } from "@/lib/types";
import { ago, amountCodes, belowMinimum, isOverlayKind } from "./activityLib";
import { eventDetails, eventName } from "./eventText";
import type { ReplayOutcome } from "./useActivity";

const KIND_ICON: Record<ActivityKind, ComponentType<{ className?: string }>> = {
  follow: UserPlus,
  subscribe: Star,
  resub: Repeat,
  gift: Gift,
  tip: Coins,
  raid: Swords,
  redemption: Ticket,
  hypetrain: Flame,
  poll: BarChart3,
  prediction: Target,
  stream: Radio,
  shoutout: Megaphone,
  moderation: ShieldAlert,
  reaction: ThumbsUp,
};

export interface ActivityFeedProps {
  events: ActivityEvent[];
  loading: boolean;
  error: string;
  forbidden: boolean;
  /** null until the server has said. */
  enabled: boolean | null;
  connected: boolean;
  /** Draws a held alert again; "gone" is the server's 404. */
  onReplay: (id: string) => Promise<ReplayOutcome>;
  /** The empty state's action: send a test alert. */
  onSendTest: () => void;
}

/** The activity feed: newest first, a row per event, filters that only change
 *  what THIS view shows. Nothing here deletes or converts anything. */
export function ActivityFeed({
  events,
  loading,
  error,
  forbidden,
  enabled,
  connected,
  onReplay,
  onSendTest,
}: Readonly<ActivityFeedProps>) {
  const t = useT();
  const [hiddenPlatforms, setHiddenPlatforms] = useState<Set<string>>(new Set());
  const [hiddenKinds, setHiddenKinds] = useState<Set<string>>(new Set());
  const [hideCode, setHideCode] = useState("");
  const [hideMin, setHideMin] = useState("");

  // Relative times need a clock; a coarse tick is enough for "12s ago".
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 10_000);
    return () => window.clearInterval(id);
  }, []);

  const platforms = useMemo(() => [...new Set(events.map((e) => e.platform))], [events]);
  const kinds = useMemo(() => [...new Set(events.map((e) => e.kind))], [events]);
  const codes = useMemo(() => amountCodes(events), [events]);

  const filter = hideCode && Number(hideMin) > 0 ? { code: hideCode, min: Number(hideMin) } : null;
  const visible = useMemo(
    () =>
      events.filter(
        (e) => !hiddenPlatforms.has(e.platform) && !hiddenKinds.has(e.kind) && !belowMinimum(e, filter),
      ),
    // `filter` is rebuilt each render from the two strings below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [events, hiddenPlatforms, hiddenKinds, hideCode, hideMin],
  );

  const flip = (set: Set<string>, v: string) => {
    const next = new Set(set);
    if (next.has(v)) next.delete(v);
    else next.add(v);
    return next;
  };

  const replay = async (e: ActivityEvent) => {
    try {
      const out = await onReplay(e.id);
      if (out === "replayed") toast.success(t("activity.replayed"));
      else toast.error(t("activity.replayGone"));
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("activity.replayFailed"));
    }
  };

  const empty = () => {
    if (forbidden) {
      return <EmptyState icon={ActivityIcon} title={t("activity.title")} body={t("activity.forbidden")} />;
    }
    if (loading && events.length === 0) {
      return <p className="p-3 text-sm text-muted-foreground">{t("activity.loading")}</p>;
    }
    if (error) {
      return <p role="alert" className="p-3 text-sm text-warn">{`${t("activity.loadFailed")} ${error}`}</p>;
    }
    if (events.length > 0) {
      return <EmptyState icon={ActivityIcon} title={t("activity.emptyFilteredTitle")} body={t("activity.emptyFiltered")} />;
    }
    return (
      <EmptyState
        icon={ActivityIcon}
        title={t("activity.emptyTitle")}
        body={enabled === false ? t("activity.emptyOff") : t("activity.emptyBody")}
        action={{ label: t("activity.sendTest"), onClick: onSendTest }}
      />
    );
  };

  return (
    <section className="flex min-h-0 flex-1 flex-col rounded-md border border-border bg-card">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-2 py-1.5">
        <ActivityIcon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
        <span className="text-sm font-semibold">{t("activity.title")}</span>
        <span className="tnum font-mono text-sm text-muted-foreground">
          {visible.length !== events.length
            ? t("chatpage.shownOfTotal", { shown: visible.length, total: events.length })
            : visible.length}
        </span>
        {!connected && !forbidden && (
          <Badge variant="warn" title={t("activity.socketOffline")}>
            {t("chatpage.socketOffline")}
          </Badge>
        )}
      </div>

      {events.length > 0 && (
        <div className="flex shrink-0 flex-col gap-1.5 border-b border-border px-2 py-1.5">
          <div role="group" aria-label={t("activity.filterPlatforms")} className="flex flex-wrap items-center gap-1">
            {platforms.map((p) => {
              const accent = accentFor(p as ChatPlatform);
              const Icon = accent.icon;
              const on = !hiddenPlatforms.has(p);
              return (
                <button
                  key={p}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setHiddenPlatforms((s) => flip(s, p))}
                  className={cn(
                    "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-sm font-semibold uppercase tracking-wider transition-colors",
                    on ? accent.chipOn : "border-border text-muted-foreground",
                  )}
                >
                  <Icon className="h-3 w-3" />
                  {accent.label}
                </button>
              );
            })}
          </div>
          <div role="group" aria-label={t("activity.filterKinds")} className="flex flex-wrap items-center gap-1">
            {kinds.map((k) => {
              const Icon = KIND_ICON[k] ?? ActivityIcon;
              const on = !hiddenKinds.has(k);
              return (
                <button
                  key={k}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setHiddenKinds((s) => flip(s, k))}
                  className={cn(
                    "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-sm font-semibold transition-colors",
                    on ? "border-border-strong bg-secondary text-secondary-foreground" : "border-border text-muted-foreground",
                  )}
                >
                  <Icon className="h-3 w-3" />
                  {t(`activity.kind.${k}`)}
                </button>
              );
            })}
          </div>
          {codes.length > 0 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="activity-hide-code" className="text-muted-foreground">
                {t("activity.hideBelow")}
              </label>
              <select
                id="activity-hide-code"
                aria-label={t("activity.hideBelowCode")}
                value={hideCode}
                onChange={(e) => setHideCode(e.target.value)}
                className="h-8 rounded-md border border-input bg-background px-2 text-sm"
              >
                <option value="">{t("activity.hideBelowOff")}</option>
                {codes.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
              <Input
                type="number"
                min={0}
                aria-label={t("activity.hideBelowAmount")}
                value={hideMin}
                disabled={!hideCode}
                onChange={(e) => setHideMin(e.target.value)}
                className="w-24"
              />
              <span className="text-muted-foreground">{t("activity.hideBelowHint")}</span>
            </div>
          )}
        </div>
      )}

      {visible.length === 0 ? (
        <div className="flex min-h-0 flex-1 items-center justify-center p-3">{empty()}</div>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {visible.map((e) => {
            const accent = accentFor(e.platform as ChatPlatform);
            const PIcon = accent.icon;
            const KIcon = KIND_ICON[e.kind] ?? ActivityIcon;
            const details = eventDetails(t, e);
            const rel = ago(e.at, now);
            return (
              <li
                key={e.id}
                data-activity-id={e.id}
                className={cn("flex items-start gap-2 border-b border-l-2 border-b-border px-2 py-1.5", accent.rule)}
              >
                <span className="mt-0.5 flex shrink-0 items-center gap-1.5">
                  <PIcon className={cn("h-3.5 w-3.5", accent.text)} />
                  <span className="sr-only">{accent.label}</span>
                  <KIcon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
                </span>
                <div className="min-w-0 flex-1 text-sm">
                  <div className="flex flex-wrap items-baseline gap-x-2">
                    <span className="font-semibold">{eventName(t, e)}</span>
                    <span className="text-muted-foreground">{t(`activity.kind.${e.kind}`)}</span>
                    {details.map((d, i) => (
                      <span key={`${i}-${d}`} className="tnum font-mono">
                        {d}
                      </span>
                    ))}
                    {e.test && <Badge variant="outline">{t("activity.testChip")}</Badge>}
                  </div>
                  {/* Plain text: viewer-written, never HTML. */}
                  {e.message && <p className="mt-0.5 break-words text-muted-foreground">{e.message}</p>}
                </div>
                <time
                  dateTime={e.at}
                  title={timestamp(e.at)}
                  className="tnum shrink-0 font-mono text-sm text-muted-foreground"
                >
                  {rel ? t(`activity.ago.${rel.unit}`, { n: rel.n }) : dayAwareTime(e.at)}
                </time>
                {isOverlayKind(e.kind) && (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("activity.replay")}
                    title={t("activity.replay")}
                    onClick={() => void replay(e)}
                  >
                    <Repeat2 />
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
