import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Loader2, RefreshCw, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import type { ChatMute, ChatPlatform, ChatStatus, XChatModerator } from "@/lib/types";

/* ===========================================================================
   X's muted viewers and chat moderators.

   Two lists X publishes and no other platform does here: who the account has
   muted (with the end of each timed mute) and who it has appointed as chat
   moderator. Both belong to the ACCOUNT, not to a broadcast, and the card says
   so beside the moderators, because "moderator" reads as per-show everywhere
   else.

   Shown only for a connected X chat. Both calls are built from X's
   documentation and have not run against a live account, and the card says
   that too rather than looking more certain than it is.

   A moderator is added by X user id, the id every chat message already carries
   (Author.ID), so no handle lookup is needed. A failed read is reported with
   X's own words and a Refresh, never as an empty list: an empty list would
   claim nobody is muted.
   =========================================================================== */

function who(m: { userId: string; username?: string; name?: string }): string {
  if (m.name && m.username) return `${m.name} (@${m.username})`;
  if (m.username) return `@${m.username}`;
  return m.name || m.userId;
}

export function XChatModeration({ statuses }: Readonly<{ statuses: ChatStatus[] }>) {
  // One card per connected X account: the hub keeps a runner for each, and each has its own mutes and moderators.
  const xs = statuses.filter((s) => s.platform === ("x" as ChatPlatform));
  if (xs.length === 0) return null;
  return (
    <>
      {xs.map((x) => (
        <XChatModerationForAccount
          key={x.account ?? ""}
          account={x.account}
          label={xs.length > 1 ? (x.account ?? "") : ""}
        />
      ))}
    </>
  );
}

function XChatModerationForAccount({ account, label }: Readonly<{ account: string | undefined; label: string }>) {
  const t = useT();
  const [mutes, setMutes] = useState<ChatMute[] | null>(null);
  const [mutesTruncated, setMutesTruncated] = useState(false);
  const [mods, setMods] = useState<XChatModerator[] | null>(null);
  const [mutesError, setMutesError] = useState("");
  const [modsError, setModsError] = useState("");
  const [loading, setLoading] = useState(true);
  const [newId, setNewId] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const [m, mo] = await Promise.allSettled([api.getChatMutes(account), api.getChatModerators(account)]);
    if (m.status === "fulfilled") {
      setMutes(m.value.mutes);
      setMutesTruncated(m.value.truncated === true);
      setMutesError("");
    } else {
      setMutesError(m.reason instanceof Error ? m.reason.message : String(m.reason));
    }
    if (mo.status === "fulfilled") {
      setMods(mo.value.moderators);
      setModsError("");
    } else {
      setModsError(mo.reason instanceof Error ? mo.reason.message : String(mo.reason));
    }
    setLoading(false);
  }, [account]);

  useEffect(() => {
    void load();
  }, [load]);

  const act = async (done: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      toast.success(done);
      await load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const idOk = /^[1-9][0-9]{0,18}$/.test(newId.trim());

  return (
    <section
      className="flex flex-col gap-2 rounded-md border border-border bg-card p-2"
      aria-label={label ? `${t("xmod.title")} ${label}` : t("xmod.title")}
    >
      <div className="flex items-center justify-between gap-2">
        <h3 className="flex items-center gap-1 text-sm font-medium">
          <ShieldCheck className="h-3.5 w-3.5" />
          {t("xmod.title")}
          {label && <span className="text-xs font-normal text-muted-foreground">{label}</span>}
        </h3>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => void load()}
          disabled={loading || busy}
          aria-label={t("xmod.refresh")}
          title={t("xmod.refresh")}
        >
          {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
        </Button>
      </div>

      <div className="flex flex-col gap-1">
        <p className="text-xs font-medium">{t("xmod.mutesTitle")}</p>
        {mutesError && <p className="text-sm text-warn">{t("xmod.loadFailed", { reason: mutesError })}</p>}
        {!mutesError && mutes && mutes.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("xmod.mutesEmpty")}</p>
        )}
        {!mutesError && (
          <ul className="flex flex-col gap-1">
            {(mutes ?? []).map((m) => (
              <li key={m.userId} className="flex items-center justify-between gap-2 text-sm">
                <span className="min-w-0 truncate">
                  {who(m)}
                  <span className="ml-1 text-xs text-muted-foreground">
                    {m.endsAt
                      ? t("xmod.muteUntil", { time: timestamp(m.endsAt) })
                      : t("xmod.muteIndefinite")}
                  </span>
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void act(t("xmod.unmuted", { name: who(m) }), () =>
                      api.unbanChatUser({ platform: "x" as ChatPlatform, account, userId: m.userId }),
                    )
                  }
                >
                  {t("xmod.unmute")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        {!mutesError && mutesTruncated && (
          <p className="text-sm text-warn">{t("xmod.mutesTruncated", { count: (mutes ?? []).length })}</p>
        )}
        <p className="text-xs text-muted-foreground">{t("xmod.mutesOwnerOnly")}</p>
      </div>

      <div className="flex flex-col gap-1">
        <p className="text-xs font-medium">{t("xmod.modsTitle")}</p>
        <p className="text-xs text-muted-foreground">{t("xmod.modsAppliesAll")}</p>
        {modsError && <p className="text-sm text-warn">{t("xmod.loadFailed", { reason: modsError })}</p>}
        {!modsError && mods && mods.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("xmod.modsEmpty")}</p>
        )}
        {!modsError && (
          <ul className="flex flex-col gap-1">
            {(mods ?? []).map((m) => (
              <li key={m.userId} className="flex items-center justify-between gap-2 text-sm">
                <span className="min-w-0 truncate">{who(m)}</span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void act(t("xmod.removed"), () => api.removeChatModerator(account, m.userId))}
                >
                  {t("xmod.remove")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <Label htmlFor={`x-mod-id-${account ?? ""}`} className="text-xs">
          {t("xmod.addLabel")}
        </Label>
        <div className="flex gap-1">
          <Input
            id={`x-mod-id-${account ?? ""}`}
            inputMode="numeric"
            value={newId}
            placeholder={t("xmod.addPlaceholder")}
            onChange={(e) => setNewId(e.target.value)}
          />
          <Button
            size="sm"
            disabled={busy || !idOk}
            onClick={() =>
              void act(t("xmod.added"), async () => {
                await api.addChatModerator(account, newId.trim());
                setNewId("");
              })
            }
          >
            {t("xmod.add")}
          </Button>
        </div>
        {newId.trim() !== "" && !idOk && <p className="text-xs text-warn">{t("xmod.idInvalid")}</p>}
      </div>
      <p className="text-xs text-muted-foreground">{t("xmod.unverified")}</p>
    </section>
  );
}
