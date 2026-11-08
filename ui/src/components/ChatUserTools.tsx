import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { ExternalLink, Loader2, ShieldCheck, ShieldOff, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { clampCp } from "@/lib/codePoints";
import { WIRE } from "@/lib/contract/wire";
import { useT } from "@/lib/i18n";
import { openPlatformLink, platformLinkFor } from "@/lib/platformLinks";
import { cachedHolders, invalidateRoles, storeHolders } from "@/lib/viewerRoles";
import type { ChatPlatform } from "@/lib/types";

/* The platform-specific tools on the viewer card.

   Every control here reuses a call another panel already makes (the Twitch
   channel tools, the X and YouTube /chat/moderators routes); this file adds no
   server logic. Only the platforms that have the tools show them: Twitch,
   X and YouTube. Anything else renders nothing.

   Role changes (VIP, moderator) ask a second time inline, the card's own
   pattern for the irreversible ban, and a refusal is shown in the server's own
   words, because "VIPs need Affiliate" or "only the broadcaster can do this" is
   the part the operator cannot guess. */

type Role = "vip" | "moderator";
type Status = boolean | null;

/* YouTube lists at most the first 50 moderators (internal/chat/moderators.go). */
const YOUTUBE_LIST_PAGE = 50;

async function holders(platform: ChatPlatform, account: string | undefined, role: Role): Promise<Set<string>> {
  const hit = cachedHolders(platform, account, role);
  if (hit) return hit;
  let ids: Set<string>;
  if (platform === "twitch") {
    const res = await api.twitchTool.read<{ holders: { userId: string }[] }>("roles", account, { role });
    ids = new Set(res.holders.map((h) => h.userId));
  } else {
    const res = await api.getChatModerators(account);
    ids = new Set(res.moderators.map((m) => m.userId ?? ""));
  }
  storeHolders(platform, account, role, ids);
  return ids;
}

export function ChatUserTools({
  platform,
  account,
  authorId,
  authorName,
  channel,
}: Readonly<{
  platform: ChatPlatform;
  account?: string;
  authorId: string;
  authorName: string;
  /** The Twitch channel the card was opened from, for the moderator-card link. */
  channel?: string;
}>) {
  const t = useT();
  const [status, setStatus] = useState<Record<Role, Status>>({ vip: null, moderator: null });
  const [busy, setBusy] = useState("");
  const [confirm, setConfirm] = useState<string | null>(null);
  const [problem, setProblem] = useState("");
  const [reason, setReason] = useState("");

  const supported = platform === "twitch" || platform === "x" || platform === "youtube";

  // Status where a read is free: Twitch's role lists, X's moderator list.
  // YouTube's list costs 50 quota units, so it is read only when removing.
  // A generation counter, not a flag: a read that was started for the viewer
  // before this one must not write its answer over this viewer's card.
  const generation = useRef(0);
  const readStatus = useCallback(async () => {
    const mine = ++generation.current;
    if (!authorId) return;
    const next: Record<Role, Status> = { vip: null, moderator: null };
    const roles: Role[] = platform === "twitch" ? ["moderator", "vip"] : platform === "x" ? ["moderator"] : [];
    await Promise.all(
      roles.map(async (r) => {
        try {
          next[r] = (await holders(platform, account, r)).has(authorId);
        } catch {
          next[r] = null;
        }
      }),
    );
    if (generation.current === mine) setStatus(next);
  }, [platform, account, authorId]);

  useEffect(() => {
    setConfirm(null);
    setProblem("");
    setReason("");
    setStatus({ vip: null, moderator: null });
    void readStatus();
    // The ref object is stable; holding it here keeps the cleanup from reading
    // .current through the component's ref at unmount time.
    const gen = generation;
    return () => {
      // Switching viewer (or closing) retires any read still in flight.
      gen.current++;
    };
  }, [readStatus]);

  if (!supported) return null;

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setProblem("");
    try {
      await fn();
      toast.success(t("viewerCard.done", { action: label }));
    } catch (err) {
      // The server's sentence, unparaphrased: it names the tier or owner rule.
      const msg = err instanceof Error && err.message ? err.message : t("viewerCard.failed", { action: label });
      setProblem(msg);
      toast.error(msg);
    } finally {
      // Success or refusal, the question has been answered: the next press asks
      // again, and what was remembered about this channel's roles is dropped (a
      // refused call may still have changed something) before status is re-read.
      invalidateRoles(platform, account);
      setConfirm(null);
      setBusy("");
      void readStatus();
    }
  };

  const roleName = (r: Role) => (r === "vip" ? t("viewerCard.roleVip") : t("viewerCard.roleModerator"));
  const statusWord = (s: Status) =>
    s === null ? t("viewerCard.statusUnknown") : s ? t("viewerCard.statusYes") : t("viewerCard.statusNo");

  /* One change that asks again: a button that, pressed, becomes a confirm/cancel
     pair. Role changes and the warning share it. */
  const roleControl = (
    id: string,
    label: string,
    destructive: boolean,
    fn: () => Promise<unknown>,
    opts: { disabled?: boolean } = {},
  ) => {
    if (confirm === id) {
      return (
        <span className="inline-flex items-center gap-1" key={id}>
          <Button
            size="sm"
            variant={destructive ? "destructive" : "default"}
            disabled={busy !== ""}
            onClick={() => void run(label, fn)}
          >
            {busy === label && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
            {t("viewerCard.confirm", { action: label })}
          </Button>
          <Button size="sm" variant="ghost" disabled={busy !== ""} onClick={() => setConfirm(null)}>
            {t("common.cancel")}
          </Button>
        </span>
      );
    }
    let icon = <ShieldOff className="mr-1 h-3 w-3" />;
    if (id === "warn") icon = <TriangleAlert className="mr-1 h-3 w-3" />;
    else if (id.startsWith("add")) icon = <ShieldCheck className="mr-1 h-3 w-3" />;
    return (
      <Button
        key={id}
        size="sm"
        variant="secondary"
        disabled={busy !== "" || !authorId || opts.disabled}
        title={t("viewerCard.asksAgain")}
        onClick={() => setConfirm(id)}
      >
        {icon}
        {label}
      </Button>
    );
  };

  // A role button for a known status shows the one move that makes sense; for
  // an unknown one (the list could not be read) both are offered.
  const roleButtons = (role: Role, add: () => Promise<unknown>, remove: () => Promise<unknown>) => {
    const s = status[role];
    const label = (adding: boolean) =>
      role === "vip"
        ? adding
          ? t("viewerCard.makeVip")
          : t("viewerCard.removeVip")
        : adding
          ? t("viewerCard.makeModerator")
          : t("viewerCard.removeModerator");
    return (
      <>
        {s !== true && roleControl(`add-${role}`, label(true), false, add)}
        {s !== false && roleControl(`remove-${role}`, label(false), true, remove)}
      </>
    );
  };

  const twitchCard = platform === "twitch" && channel
    ? platformLinkFor({
        id: "",
        platform: "twitch",
        channel,
        author: { id: authorId, name: authorName },
        text: "",
        at: "",
      })
    : null;

  const warnLimit = WIRE.twitchTools.maxWarning;

  return (
    <div className="flex flex-col gap-2" data-testid="chat-user-tools">
      <span className="text-sm uppercase tracking-wider text-muted-foreground">{t("viewerCard.toolsTitle")}</span>

      {platform === "twitch" && (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
            <input
              className="h-8 min-w-0 flex-1 rounded border border-border bg-card-raised px-2 text-[12px]"
              value={reason}
              placeholder={t("viewerCard.warnPlaceholder")}
              aria-label={t("viewerCard.warnPlaceholder")}
              onChange={(e) => setReason(clampCp(e.target.value, warnLimit))}
            />
            {roleControl(
              "warn",
              t("viewerCard.warn"),
              false,
              async () => {
                await api.twitchTool.post("warnings", account, { userId: authorId, reason: reason.trim() });
                setReason("");
              },
              { disabled: reason.trim() === "" },
            )}
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-sm text-muted-foreground">
              {t("viewerCard.status", { role: roleName("vip"), status: statusWord(status.vip) })}
            </span>
            {roleButtons(
              "vip",
              () => api.twitchTool.post("roles", account, { role: "vip", userId: authorId }),
              () => api.twitchTool.remove("roles", account, { role: "vip", userId: authorId }),
            )}
          </div>
        </>
      )}

      {(platform === "twitch" || platform === "x") && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm text-muted-foreground">
            {t("viewerCard.status", { role: roleName("moderator"), status: statusWord(status.moderator) })}
          </span>
          {platform === "twitch"
            ? roleButtons(
                "moderator",
                () => api.twitchTool.post("roles", account, { role: "moderator", userId: authorId }),
                () => api.twitchTool.remove("roles", account, { role: "moderator", userId: authorId }),
              )
            : roleButtons(
                "moderator",
                () => api.addChatModerator(account, authorId),
                () => api.removeChatModerator(account, authorId),
              )}
        </div>
      )}

      {platform === "youtube" && (
        <div className="flex flex-wrap items-center gap-1.5">
          {roleControl(
            "add-moderator",
            t("viewerCard.makeModerator"),
            false,
            () => api.addYouTubeChatModerator({ platform, account, channelId: authorId }),
          )}
          {roleControl(
            "remove-moderator",
            t("viewerCard.removeModerator"),
            true,
            async () => {
              // Removal needs the moderator id the list returned, not the
              // channel id. The read is made here, once, on a confirmed press.
              const list = await api.youTubeChatModerators({ platform, account });
              const mine = list.moderators.find((m) => m.channelId === authorId);
              if (!mine?.id) {
                // The list is only YouTube's first page. A full page means the
                // viewer may be further down, which is not the same as "not one".
                throw new Error(
                  list.moderators.length >= YOUTUBE_LIST_PAGE
                    ? t("viewerCard.notInFirstPage", { count: YOUTUBE_LIST_PAGE })
                    : t("viewerCard.notAModerator"),
                );
              }
              await api.removeYouTubeChatModerator({ platform, account, id: mine.id });
            },
          )}
        </div>
      )}

      {platform === "x" && <p className="text-sm text-muted-foreground">{t("viewerCard.xAllBroadcasts")}</p>}
      {platform === "youtube" && <p className="text-sm text-muted-foreground">{t("viewerCard.youtubeRemoveCost")}</p>}

      {twitchCard && (
        <div>
          <Button size="sm" variant="ghost" title={t(twitchCard.caveat)} onClick={() => openPlatformLink(twitchCard)}>
            <ExternalLink className="mr-1 h-3 w-3" />
            {t("viewerCard.openTwitchCard")}
          </Button>
        </div>
      )}

      {problem && (
        <p role="alert" className="text-sm text-warn">
          {problem}
        </p>
      )}
    </div>
  );
}
