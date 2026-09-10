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

   ALL EIGHT of the settings the server can write are here. Four of them were
   not: emote-only, the moderator delay, and the follow-age that follower-only
   mode is actually about. ChatSettings carried them, api.updateChatSettings
   sent them, TwitchAdapter.UpdateChatSettings mapped them onto Helix, and no
   operator could ask for any of them — the exact shape of unreachability this
   audit is about, and the worst four to be missing: emote-only and a moderator
   delay are what a channel reaches for during a raid, and follower-only with
   no minimum is the mode a raider walks straight through by pressing Follow.

   Nothing here reads the platform's CURRENT state, and that is a deliberate
   limitation rather than an oversight: polyemesis does not fetch chat settings,
   so it does not know whether slow mode is already on. Every switch therefore
   starts off and means "turn this on now", which is why the copy says "Apply"
   and not "Save" — the difference between issuing a command and describing a
   state. A toggle that showed a default-off position as though it were the
   channel's real state would be lying about something the operator can check in
   one glance on Twitch.
   =========================================================================== */

/** The only three values Twitch's non_moderator_chat_delay_duration accepts.
 *
 *  A fixed list rather than a number input, because Helix rejects anything
 *  else and names no field when it does: an operator who typed 5 would get
 *  "Bad Request" from a PATCH that also carried whatever else they had
 *  switched on, and no way to tell which part it disliked. Make the wrong
 *  value impossible to enter. */
const MOD_DELAYS = [2, 4, 6];

/** Twitch's ceiling on follower_mode_duration: three months, in minutes. */
const MAX_FOLLOWER_MINUTES = 129600;

export function ChatRules({ statuses }: { statuses: ChatStatus[] }) {
  // Twitch only, and by connection rather than by hope: an account that is not
  // attached cannot take a settings write, and the button would 400.
  const twitch = statuses.find((s) => s.platform === ("twitch" as ChatPlatform));
  const [slow, setSlow] = useState(false);
  const [slowSeconds, setSlowSeconds] = useState(30);
  const [followers, setFollowers] = useState(false);
  // Twitch's follower_mode_duration, in minutes. Zero is a real value and
  // means "any follower, however new", which is what follower-only mode did
  // before this control existed — so it is the default, and switching the mode
  // on without touching this behaves exactly as it used to.
  const [followerMinutes, setFollowerMinutes] = useState(0);
  const [subsOnly, setSubsOnly] = useState(false);
  const [emotesOnly, setEmotesOnly] = useState(false);
  const [unique, setUnique] = useState(false);
  const [modDelay, setModDelay] = useState(false);
  // Twitch accepts 2, 4 or 6 and nothing else here; see MOD_DELAYS below.
  const [modDelaySeconds, setModDelaySeconds] = useState(4);
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
    if (followers) {
      body.followerMode = true;
      // Sent WITH the mode, always. Twitch requires the paired duration
      // whenever a mode is enabled and rejects the request naming neither
      // field when it is missing — the same pairing slowMode already has.
      body.followerModeMinutes = followerMinutes;
    }
    if (subsOnly) body.subscriberMode = true;
    if (emotesOnly) body.emoteMode = true;
    if (unique) body.uniqueChatMode = true;
    if (modDelay) {
      body.nonModeratorChatDelay = true;
      body.nonModeratorChatDelaySeconds = modDelaySeconds;
    }

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
        // The two that were missing from here as well as from the switches
        // above. "Turn all off" that leaves emote-only and the moderator delay
        // running is the worst possible answer to a button with that label:
        // the operator reads the room as reopened and it is not.
        emoteMode: false,
        uniqueChatMode: false,
        nonModeratorChatDelay: false,
      });
      setSlow(false);
      setFollowers(false);
      setSubsOnly(false);
      setEmotesOnly(false);
      setUnique(false);
      setModDelay(false);
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
          {/* THE AGE, which is the whole point of the mode during a raid.
              Follower-only with no minimum is a door a raider walks through by
              pressing Follow, and that is what this control's absence meant:
              followerModeMinutes existed on ChatSettings and reached Helix,
              and the only value it could ever have was Twitch's own default.
              Shown only when the mode is on, like the slow-mode seconds
              above — a duration for a mode nobody switched on is a number with
              nothing to modify. */}
          {followers && (
            <input
              type="number"
              min={0}
              max={MAX_FOLLOWER_MINUTES}
              value={followerMinutes}
              disabled={busy}
              onChange={(e) =>
                setFollowerMinutes(
                  Math.min(MAX_FOLLOWER_MINUTES, Math.max(0, Number(e.target.value) || 0)),
                )
              }
              aria-label="Minutes they must have followed for"
              className="tnum h-6 w-16 rounded border border-border bg-card-raised px-1 text-[11px]"
            />
          )}
          {followers && (
            <span className="text-[10px] text-subtle-foreground">
              {followerMinutes === 0 ? "minutes — any follower" : "minutes"}
            </span>
          )}
        </div>

        <div className="flex items-center gap-1.5">
          <Switch id="rule-subs" checked={subsOnly} onCheckedChange={setSubsOnly} disabled={busy} />
          <Label htmlFor="rule-subs" className="text-[11px]">
            Subscribers only
          </Label>
        </div>

        {/* The blunt instrument, and during a raid the one that works: nothing
            but emotes can carry a slur or a link. */}
        <div className="flex items-center gap-1.5">
          <Switch
            id="rule-emotes"
            checked={emotesOnly}
            onCheckedChange={setEmotesOnly}
            disabled={busy}
          />
          <Label htmlFor="rule-emotes" className="text-[11px]">
            Emotes only
          </Label>
        </div>

        <div className="flex items-center gap-1.5">
          <Switch id="rule-unique" checked={unique} onCheckedChange={setUnique} disabled={busy} />
          <Label htmlFor="rule-unique" className="text-[11px]">
            No repeats
          </Label>
        </div>

        {/* Holds every non-moderator message for a few seconds so a moderator
            sees it before the audience does. The one rule here that buys TIME
            rather than restricting who may speak, which is why it is worth a
            control of its own rather than being folded in with slow mode. */}
        <div className="flex items-center gap-1.5">
          <Switch
            id="rule-mod-delay"
            checked={modDelay}
            onCheckedChange={setModDelay}
            disabled={busy}
          />
          <Label htmlFor="rule-mod-delay" className="text-[11px]">
            Moderator delay
          </Label>
          {modDelay && (
            /* A FIXED LIST, not a number box. Helix accepts 2, 4 or 6 and
               rejects everything else with a plain Bad Request that names no
               field — so a typed 5 would fail a PATCH also carrying whatever
               else the operator had switched on, with nothing to say which
               part was wrong. */
            <select
              value={modDelaySeconds}
              disabled={busy}
              onChange={(e) => setModDelaySeconds(Number(e.target.value))}
              aria-label="Seconds moderators get to see a message first"
              className="tnum h-6 rounded border border-border bg-card-raised px-1 text-[11px]"
            >
              {MOD_DELAYS.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          )}
          {modDelay && <span className="text-[10px] text-subtle-foreground">seconds</span>}
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
