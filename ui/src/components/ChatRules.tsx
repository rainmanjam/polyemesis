import { useState } from "react";
import { toast } from "sonner";
import { Loader2, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { accentFor } from "@/lib/chat";
import type { ChatPlatform, ChatSettings, ChatStatus } from "@/lib/types";

/* ===========================================================================
   Channel-wide chat rules: slow mode, follower-only, and the rest.

   These act on the ROOM, not on a message or a person, which is why they are a
   separate control from the user card rather than another row of buttons in it.
   Offering "slow mode" beside "ban" would present two very different decisions
   as the same kind of thing.

   Only Twitch publishes an API for any of this. The control appears only for a
   connected Twitch account, and the absence elsewhere is explained in the
   capability matrix rather than by an inert switch nobody can move.

   Nothing here reads the platform's CURRENT state, and that is a deliberate
   limitation rather than an oversight: polyemesis does not fetch chat settings,
   so it does not know whether slow mode is already on. Every switch therefore
   starts off and means "turn this on now", which is why the copy says "Apply"
   and not "Save" — the difference between issuing a command and describing a
   state. A toggle that showed a default-off position as though it were the
   channel's real state would be lying about something the operator can check in
   one glance on Twitch.
   =========================================================================== */

export function ChatRules({ statuses }: { statuses: ChatStatus[] }) {
  // Twitch only, and by connection rather than by hope: an account that is not
  // attached cannot take a settings write, and the button would 400.
  const twitch = statuses.find((s) => s.platform === ("twitch" as ChatPlatform));
  const [slow, setSlow] = useState(false);
  const [slowSeconds, setSlowSeconds] = useState(30);
  const [followers, setFollowers] = useState(false);
  const [subsOnly, setSubsOnly] = useState(false);
  const [unique, setUnique] = useState(false);
  const [busy, setBusy] = useState(false);

  if (!twitch) return null;

  const apply = async () => {
    setBusy(true);
    // Only what the operator actually switched on is sent. An omitted field
    // means "leave it alone" all the way down to Twitch's PATCH, so this cannot
    // switch off a mode the operator never touched.
    const body: ChatSettings = {};
    if (slow) {
      body.slowMode = true;
      body.slowModeSeconds = slowSeconds;
    }
    if (followers) body.followerMode = true;
    if (subsOnly) body.subscriberMode = true;
    if (unique) body.uniqueChatMode = true;

    try {
      await api.updateChatSettings("twitch" as ChatPlatform, twitch.account, body);
      toast.success("Chat rules applied on Twitch.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not change the chat rules.");
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setBusy(true);
    try {
      // Explicit falses: this is the one call that MEANS "off", so it says so
      // rather than relying on absence, which means the opposite.
      await api.updateChatSettings("twitch" as ChatPlatform, twitch.account, {
        slowMode: false,
        followerMode: false,
        subscriberMode: false,
        uniqueChatMode: false,
      });
      setSlow(false);
      setFollowers(false);
      setSubsOnly(false);
      setUnique(false);
      toast.success("Chat rules cleared on Twitch.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not clear the chat rules.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-card p-2">
      <div className="flex items-center gap-2">
        <SlidersHorizontal className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-[12px] font-semibold">Chat rules</span>
        <span className={`text-[10px] ${accentFor("twitch" as ChatPlatform).text}`}>
          Twitch only
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <div className="flex items-center gap-1.5">
          <Switch id="rule-slow" checked={slow} onCheckedChange={setSlow} disabled={busy} />
          <Label htmlFor="rule-slow" className="text-[11px]">
            Slow mode
          </Label>
          {slow && (
            <input
              type="number"
              min={1}
              max={120}
              value={slowSeconds}
              disabled={busy}
              onChange={(e) => setSlowSeconds(Math.max(1, Number(e.target.value) || 1))}
              aria-label="Seconds between messages"
              className="tnum h-6 w-14 rounded border border-border bg-card-raised px-1 text-[11px]"
            />
          )}
          {slow && <span className="text-[10px] text-subtle-foreground">seconds</span>}
        </div>

        <div className="flex items-center gap-1.5">
          <Switch
            id="rule-followers"
            checked={followers}
            onCheckedChange={setFollowers}
            disabled={busy}
          />
          <Label htmlFor="rule-followers" className="text-[11px]">
            Followers only
          </Label>
        </div>

        <div className="flex items-center gap-1.5">
          <Switch id="rule-subs" checked={subsOnly} onCheckedChange={setSubsOnly} disabled={busy} />
          <Label htmlFor="rule-subs" className="text-[11px]">
            Subscribers only
          </Label>
        </div>

        <div className="flex items-center gap-1.5">
          <Switch id="rule-unique" checked={unique} onCheckedChange={setUnique} disabled={busy} />
          <Label htmlFor="rule-unique" className="text-[11px]">
            No repeats
          </Label>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => void apply()} disabled={busy}>
          {busy && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
          Apply
        </Button>
        <Button size="sm" variant="secondary" onClick={() => void clear()} disabled={busy}>
          Turn all off
        </Button>
        {/* The limitation, said out loud. See the note at the top of this file. */}
        <span className="text-[10px] text-subtle-foreground">
          Applies what is switched on here; it does not read Twitch's current settings back.
        </span>
      </div>
    </div>
  );
}
