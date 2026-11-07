import { useState } from "react";
import { toast } from "sonner";
import { Loader2, UserMinus, UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { ChatModerator, ChatPlatform, ChatStatus } from "@/lib/types";

/* ===========================================================================
   Who may moderate the YouTube chat: liveChatModerators list, insert, delete.

   Three things shape this card.

   It reads ONLY on request. A read costs 50 quota units against the shared
   daily allowance, so opening the Chat page must not spend them; the operator
   presses Load.

   Only the owner of the chat's channel may call it. YouTube answers anyone else
   with a 403, and the server turns that into a sentence which is shown as-is.

   A moderator is added by CHANNEL ID (UC...), because that is what the API
   takes. There is no name lookup, so the field says what it wants.
   =========================================================================== */

export function ChatModerators({ statuses }: Readonly<{ statuses: ChatStatus[] }>) {
  const yt = statuses.find((s) => s.platform === ("youtube" as ChatPlatform));
  if (!yt) return null;
  // Keyed on the account, so switching accounts never shows one channel's
  // moderators against another's chat.
  return <ChatModeratorsForAccount key={yt.account ?? ""} account={yt.account} />;
}

function ChatModeratorsForAccount({ account }: Readonly<{ account: string | undefined }>) {
  const t = useT();
  const [mods, setMods] = useState<ChatModerator[] | null>(null);
  const [channelId, setChannelId] = useState("");
  const [busy, setBusy] = useState("");
  const platform = "youtube" as ChatPlatform;

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    try {
      await fn();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("chatMods.failed"));
    } finally {
      setBusy("");
    }
  };

  const load = () =>
    run("load", async () => {
      setMods((await api.chatModerators({ platform, account })).moderators);
    });

  const add = () =>
    run("add", async () => {
      const m = await api.addChatModerator({ platform, account, channelId: channelId.trim() });
      setMods((cur) => [...(cur ?? []).filter((x) => x.id !== m.id), m]);
      setChannelId("");
      toast.success(t("chatMods.added", { name: m.displayName || m.channelId }));
    });

  const remove = (m: ChatModerator) =>
    run(`rm-${m.id}`, async () => {
      await api.removeChatModerator({ platform, account, id: m.id });
      setMods((cur) => (cur ?? []).filter((x) => x.id !== m.id));
      toast.success(t("chatMods.removed", { name: m.displayName || m.channelId }));
    });

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-card p-2" data-testid="chat-moderators">
      <div className="flex items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-sm font-medium">
          <Users className="h-3.5 w-3.5" /> {t("chatMods.title")}
        </p>
        <Button size="sm" variant="secondary" disabled={busy !== ""} onClick={load}>
          {busy === "load" && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
          {mods === null ? t("chatMods.load") : t("chatMods.reload")}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">{t("chatMods.help")}</p>

      {mods !== null && mods.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("chatMods.none")}</p>
      )}
      {mods !== null && mods.length > 0 && (
        <ul className="flex flex-col gap-1">
          {mods.map((m) => (
            <li key={m.id} className="flex items-center justify-between gap-2 text-sm">
              <span className="truncate" title={m.channelId}>
                {m.displayName || m.channelId}
              </span>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy !== ""}
                aria-label={t("chatMods.removeAria", { name: m.displayName || m.channelId })}
                onClick={() => void remove(m)}
              >
                <UserMinus className="h-3 w-3" />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <div className="flex items-center gap-1.5">
        <Input
          value={channelId}
          placeholder={t("chatMods.channelIdPlaceholder")}
          aria-label={t("chatMods.channelIdLabel")}
          onChange={(e) => setChannelId(e.target.value)}
        />
        <Button size="sm" disabled={busy !== "" || channelId.trim() === ""} onClick={add}>
          {busy === "add" ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : <UserPlus className="mr-1 h-3 w-3" />}
          {t("chatMods.add")}
        </Button>
      </div>
    </div>
  );
}
