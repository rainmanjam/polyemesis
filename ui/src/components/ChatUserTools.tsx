import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { ExternalLink, Loader2, ShieldCheck, ShieldOff, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { WIRE } from "@/lib/contract/wire";
import { useT } from "@/lib/i18n";
import { openPlatformLink, platformLinkFor } from "@/lib/platformLinks";
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

/* Who holds a role is a list call per role, so it is remembered for a minute
   per channel rather than re-read on every card. A change made from here
   drops the entry. */
const ROLE_TTL_MS = 60_000;
const roleCache = new Map<string, { at: number; ids: Set<string> }>();

const roleKey = (platform: string, account: string | undefined, role: string) => `${platform}|${account ?? ""}|${role}`;

/** Test seam. */
export function resetRoleCache(): void {
  roleCache.clear();
}

async function holders(platform: ChatPlatform, account: string | undefined, role: Role): Promise<Set<string>> {
  const key = roleKey(platform, account, role);
  const hit = roleCache.get(key);
  if (hit && Date.now() - hit.at < ROLE_TTL_MS) return hit.ids;
  let ids: Set<string>;
  if (platform === "twitch") {
    const res = await api.twitchTool.read<{ holders: { userId: string }[] }>("roles", account, { role });
    ids = new Set(res.holders.map((h) => h.userId));
  } else {
    const res = await api.getChatModerators(account);
    ids = new Set(res.moderators.map((m) => m.userId ?? ""));
  }
  roleCache.set(key, { at: Date.now(), ids });
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
  const readStatus = useCallback(async () => {
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
    setStatus(next);
  }, [platform, account, authorId]);

  useEffect(() => {
    setConfirm(null);
    setProblem("");
    setReason("");
    void readStatus();
  }, [readStatus]);

  if (!supported) return null;

  const run = async (label: string, fn: () => Promise<unknown>, forget?: Role) => {
    setBusy(label);
    setProblem("");
    try {
      await fn();
      if (forget) roleCache.delete(roleKey(platform, account, forget));
      toast.success(t("viewerCard.done", { action: label }));
      await readStatus();
    } catch (err) {
      // The server's sentence, unparaphrased: it names the tier or owner rule.
      const msg = err instanceof Error && err.message ? err.message : t("viewerCard.failed", { action: label });
      setProblem(msg);
      toast.error(msg);
    } finally {
      // Success or refusal, the question has been answered: the next press asks again.
      setConfirm(null);
      setBusy("");
    }
  };

  const roleName = (r: Role) => (r === "vip" ? t("viewerCard.roleVip") : t("viewerCard.roleModerator"));
  const statusWord = (s: Status) =>
    s === null ? t("viewerCard.statusUnknown") : s ? t("viewerCard.statusYes") : t("viewerCard.statusNo");

  /* One role change: a button that, pressed, becomes a confirm/cancel pair. */
  const roleControl = (id: string, label: string, destructive: boolean, fn: () => Promise<unknown>, role: Role) => {
    if (confirm === id) {
      return (
        <span className="inline-flex items-center gap-1" key={id}>
          <Button
            size="sm"
            variant={destructive ? "destructive" : "default"}
            disabled={busy !== ""}
            onClick={() => void run(label, fn, role)}
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
    return (
      <Button
        key={id}
        size="sm"
        variant="secondary"
        disabled={busy !== "" || !authorId}
        title={t("viewerCard.asksAgain")}
        onClick={() => setConfirm(id)}
      >
        {id.startsWith("add") ? <ShieldCheck className="mr-1 h-3 w-3" /> : <ShieldOff className="mr-1 h-3 w-3" />}
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
        {s !== true && roleControl(`add-${role}`, label(true), false, add, role)}
        {s !== false && roleControl(`remove-${role}`, label(false), true, remove, role)}
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
              maxLength={warnLimit}
              placeholder={t("viewerCard.warnPlaceholder")}
              aria-label={t("viewerCard.warnPlaceholder")}
              onChange={(e) => setReason(e.target.value)}
            />
            <Button
              size="sm"
              variant="secondary"
              disabled={busy !== "" || !authorId || reason.trim() === ""}
              onClick={() =>
                void run(t("viewerCard.warn"), async () => {
                  await api.twitchTool.post("warnings", account, { userId: authorId, reason: reason.trim() });
                  setReason("");
                })
              }
            >
              {busy === t("viewerCard.warn") ? (
                <Loader2 className="mr-1 h-3 w-3 animate-spin" />
              ) : (
                <TriangleAlert className="mr-1 h-3 w-3" />
              )}
              {t("viewerCard.warn")}
            </Button>
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
            "moderator",
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
              if (!mine?.id) throw new Error(t("viewerCard.notAModerator"));
              await api.removeYouTubeChatModerator({ platform, account, id: mine.id });
            },
            "moderator",
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
