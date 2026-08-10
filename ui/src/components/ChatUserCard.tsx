import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Ban, EyeOff, Loader2, Shield, Timer, Undo2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { accentFor, TIMEOUTS } from "@/lib/chat";
import { capabilityFor, supportOf } from "@/lib/capabilities";
import { clockTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { ChatMessage, ChatPlatform, ChatUserCard as CardData } from "@/lib/types";

/* ===========================================================================
   The moderator's user card: what this person has said, and what can be done
   about it.

   Twitch has one of these and it is the thing moderators actually use — you
   click a name and see whether one bad message was a bad moment or a pattern.
   No platform publishes an API for it. Twitch's is a web-app feature over
   internal endpoints; Helix offers "who is here now" and "who are the
   moderators", neither of which is a history. YouTube, Kick and Facebook have
   nothing comparable.

   polyemesis does not need one, because it already stores every message with an
   author id on all four platforms. That makes this card work IDENTICALLY
   everywhere — something Twitch's own cannot do — at the cost of depth, which is
   why the retention note is rendered rather than tucked into a tooltip.

   Every action here is destructive to some degree, so the ordering is
   deliberate: least damage first, and the two that cannot be undone are last and
   ask again.
   =========================================================================== */

export function ChatUserCard({
  platform,
  account,
  authorId,
  authorName,
  open,
  onOpenChange,
}: {
  platform: ChatPlatform;
  account?: string;
  authorId: string;
  authorName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [card, setCard] = useState<CardData | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState("");
  // A permanent ban asks twice. Everything else does not: friction has to be
  // proportional to the consequence, and a confirmation on every action trains
  // people to click through the one that matters.
  const [confirmBan, setConfirmBan] = useState(false);

  const load = useCallback(() => {
    if (!authorId) return;
    setLoading(true);
    api
      .chatUser({ platform, authorId, limit: 200 })
      .then(setCard)
      .catch(() => setCard(null))
      .finally(() => setLoading(false));
  }, [platform, authorId]);

  useEffect(() => {
    if (open) {
      setConfirmBan(false);
      load();
    }
  }, [open, load]);

  // What this platform can actually do, from the same matrix the settings page
  // renders. An action the platform cannot perform is shown DISABLED with the
  // reason rather than hidden: a moderator who cannot find the timeout button
  // does not conclude "Facebook has no timeouts", they conclude the tool is
  // broken.
  const caps = capabilityFor(platform);
  const canModerate = supportOf(caps, "moderation") === "yes";

  const run = async (what: string, fn: () => Promise<{ detail?: string } | void>) => {
    setBusy(what);
    try {
      const res = await fn();
      // The server's sentence, not ours. It carries the difference between
      // "hidden from viewers" and "hidden only here" — which the UI must not
      // paraphrase, because paraphrasing is how that distinction gets lost.
      const detail = res && "detail" in res ? res.detail : undefined;
      toast.success(detail || `${what} applied`);
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `${what} failed`);
    } finally {
      setBusy("");
    }
  };

  const messages = card?.messages ?? [];
  const accent = accentFor(platform);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span style={card?.color ? { color: card.color } : undefined}>
              {card?.name || authorName}
            </span>
            {card?.broadcaster && <Badge variant="armed">Broadcaster</Badge>}
            {card?.moderator && (
              <Badge variant="armed">
                <Shield className="mr-0.5 h-2.5 w-2.5" />
                Moderator
              </Badge>
            )}
            {card?.subscriber && <Badge variant="outline">Subscriber</Badge>}
            <Badge variant="outline" className={accent.text}>
              {accent.label}
            </Badge>
          </DialogTitle>
          <DialogDescription>
            {loading
              ? "Reading this server's scrollback…"
              : `${messages.length}${card?.truncated ? "+" : ""} message${
                  messages.length === 1 ? "" : "s"
                } on record.`}
          </DialogDescription>
        </DialogHeader>

        {/* ---- what they said ---- */}
        <div className="max-h-64 overflow-y-auto rounded border border-border bg-card-raised p-2">
          {loading && messages.length === 0 ? (
            <p className="p-2 text-[11px] text-muted-foreground">
              <Loader2 className="mr-1 inline h-3 w-3 animate-spin" />
              Loading…
            </p>
          ) : messages.length === 0 ? (
            <p className="p-2 text-[11px] text-muted-foreground">
              Nothing from this person is in the scrollback. They may have spoken before this
              server started, or before its retention window.
            </p>
          ) : (
            messages.map((m: ChatMessage) => (
              <div key={m.id} className="py-0.5 text-[12px] leading-snug">
                <span className="tnum mr-1.5 font-mono text-[10px] text-subtle-foreground">
                  {clockTime(m.at)}
                </span>
                <span className="break-words">{m.text}</span>
              </div>
            ))
          )}
        </div>

        {/* The honest caveat. Rendered, not tucked away: a moderator reading
            "3 messages" as "this person has said three things ever" has been
            misled by a window that only ever showed them a slice. */}
        {card?.retentionNote && (
          <p className="text-[10px] leading-relaxed text-subtle-foreground">
            {card.truncated && <strong>Showing the most recent only. </strong>}
            {card.retentionNote}
          </p>
        )}

        {/* ---- what can be done ---- */}
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-[10px] uppercase tracking-wider text-subtle-foreground">
              Time out
            </span>
            {TIMEOUTS.map((t) => (
              <Button
                key={t.seconds}
                size="sm"
                variant="secondary"
                disabled={!canModerate || busy !== ""}
                title={
                  canModerate
                    ? `Time out for ${t.label}`
                    : `polyemesis cannot moderate ${platform}`
                }
                onClick={() =>
                  run(`Timed out for ${t.label}`, () =>
                    api.banChatUser({ platform, account, userId: authorId, seconds: t.seconds }),
                  )
                }
              >
                {busy === `Timed out for ${t.label}` ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <Timer className="mr-1 h-3 w-3" />
                )}
                {t.label}
              </Button>
            ))}
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              size="sm"
              variant="secondary"
              disabled={busy !== ""}
              title="Remove their messages from this server only. Everyone watching still sees them."
              onClick={() =>
                run("Hidden here", async () => {
                  // Local hide has no per-user route: it is per-message by
                  // design, so this applies it to what we are holding. Said
                  // plainly in the button's title rather than implied.
                  for (const m of messages) {
                    await api.hideChatMessage({
                      platform,
                      account,
                      id: m.id,
                      scope: "local",
                    });
                  }
                  return {
                    detail: `Hidden in polyemesis only. Everyone watching on ${platform} can still see them.`,
                  };
                })
              }
            >
              <EyeOff className="mr-1 h-3 w-3" />
              Hide here only
            </Button>

            <Button
              size="sm"
              variant="secondary"
              disabled={!canModerate || busy !== ""}
              onClick={() =>
                run("Ban lifted", () =>
                  api.unbanChatUser({ platform, account, userId: authorId }),
                )
              }
            >
              <Undo2 className="mr-1 h-3 w-3" />
              Lift ban
            </Button>

            {/* Last, and asks again. The only irreversible action here. */}
            {confirmBan ? (
              <>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={busy !== ""}
                  onClick={() =>
                    run("Banned", async () => {
                      const res = await api.banChatUser({
                        platform,
                        account,
                        userId: authorId,
                      });
                      onOpenChange(false);
                      return res;
                    })
                  }
                >
                  {busy === "Banned" ? (
                    <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                  ) : (
                    <Ban className="mr-1 h-3 w-3" />
                  )}
                  Ban permanently — confirm
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setConfirmBan(false)}>
                  Cancel
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="outline"
                disabled={!canModerate || busy !== ""}
                className={cn(canModerate && "text-down hover:text-down")}
                onClick={() => setConfirmBan(true)}
              >
                <Ban className="mr-1 h-3 w-3" />
                Ban permanently
              </Button>
            )}
          </div>

          {!canModerate && (
            <p className="text-[10px] text-warn">
              polyemesis cannot moderate {accent.label} — the platform publishes no API for it, or
              this account has not granted the scope. Use the {accent.label} dashboard. Hiding here
              still works, because it asks nobody's permission.
            </p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
