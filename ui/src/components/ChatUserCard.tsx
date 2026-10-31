import { useCallback, useEffect, useState } from "react";
import { useT } from "@/lib/i18n";
import { toast } from "sonner";
import { Ban, Eye, EyeOff, Loader2, Shield, Timer, Undo2 } from "lucide-react";
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
import { failedRead, mayClaim, okRead, pendingRead, readFailed, type ReadState } from "@/lib/readState";
import { accentFor, TIMEOUTS } from "@/lib/chat";
import { chatActionSupport } from "@/lib/chatModeration";
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

/** Concurrent hides per batch. Enough to stop a 200-line card taking 200 round
 *  trips, few enough not to flood the platform's moderation API. */
const HIDE_CONCURRENCY = 5;

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
  const t = useT();
  /* A CONTROL over the two claims this dialog makes about the scrollback.
   *
   * It was `.catch(() => setCard(null))`, which STORED A FAILED READ AS THE
   * SAME VALUE as "loaded, and this person has said nothing" -- and both of
   * this card's claims are read off that one value. A moderator deciding
   * whether a bad line was a bad moment or a pattern was shown
   * "0 messages on record" and "Nothing from this person is in the scrollback",
   * which is the exact shape of a first offence. The read had simply failed.
   *
   * ReadState is a type guard, so the card CANNOT reach a message list for a
   * read that did not answer: there is no value to render an empty state
   * beside. Same module the token list, the platform credentials and the
   * automation lists were moved onto. */
  const [read, setRead] = useState<ReadState<CardData>>(pendingRead());
  const card = mayClaim(read) ? read.value : null;
  const failed = readFailed(read);
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
      .then((c) => setRead(okRead(c)))
      .catch(() => setRead(failedRead()))
      .finally(() => setLoading(false));
  }, [platform, authorId]);

  useEffect(() => {
    if (open) {
      setConfirmBan(false);
      setRead(pendingRead());
      load();
    }
  }, [open, load]);

  // What this platform can actually do, ASKED ONE ACTION AT A TIME. An action
  // the platform cannot perform is shown DISABLED with the reason rather than
  // hidden: a moderator who cannot find the timeout button does not conclude
  // "Facebook has no timeouts", they conclude the tool is broken.
  //
  // It used to be one question — `supportOf(caps, "moderation") === "yes"` —
  // and that is a COLUMN in the setup page's matrix, a summary of the whole
  // platform. Facebook answers yes to it because it deletes and hides
  // comments, and implements no chat.Banner at all, so every timeout and the
  // ban rendered enabled here and failed at hub.go:803 on every press. A
  // control that lies to a moderator mid-raid is worse than one that is
  // missing. See lib/chatModeration.ts.
  const ban = chatActionSupport(platform, "ban", t);
  const hideUpstream = chatActionSupport(platform, "hideUpstream", t);

  const run = async (what: string, fn: () => Promise<{ detail?: string } | void>) => {
    setBusy(what);
    try {
      const res = await fn();
      // The server's sentence, not ours. It carries the difference between
      // "hidden from viewers" and "hidden only here" — which the UI must not
      // paraphrase, because paraphrasing is how that distinction gets lost.
      const detail = res && "detail" in res ? res.detail : undefined;
      toast.success(detail || t("chatCard.actionApplied", { action: what }));
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("chatCard.actionFailed", { action: what }));
      // A partial action has still changed some lines, so the card reloads on
      // failure too, not only on success.
      load();
    } finally {
      setBusy("");
    }
  };

  const messages = card?.messages ?? [];
  const accent = accentFor(platform);

  // Every hide on this card loops over the messages the card is HOLDING —
  // neither scope has a per-user route, both are per-message by design — so
  // all of them share one precondition and one sentence for failing it. Said
  // once here rather than three times below, because three copies is how two
  // of them end up wrong.
  const nothingToHide =
    messages.length === 0
      ? failed
        ? t("chatCard.nothingToHideFailed")
        : t("chatCard.nothingToHideEmpty")
      : "";

  /** Hides every message the card is holding, at one scope or the other.
   *
   *  The two scopes are ONE endpoint and two completely different promises to
   *  the audience, which is exactly why they are two buttons rather than a
   *  parameter nobody chooses: `scope: "local"` was hard-coded at the only
   *  call site, so Facebook's reversible upstream hide — the one thing
   *  Facebook can do that no other platform here can — was unreachable from
   *  the UI entirely. */
  const hideAll = (scope: "local" | "platform", hidden: boolean) => async () => {
    // Bounded concurrency, not one await per message: a card can hold 200 lines,
    // and a sequential loop took that many round trips to finish. Each batch
    // settles as a whole, so one failure cannot hide the others' outcomes.
    let count = 0;
    let failed = 0;
    for (let i = 0; i < messages.length; i += HIDE_CONCURRENCY) {
      const batch = messages.slice(i, i + HIDE_CONCURRENCY);
      const settled = await Promise.allSettled(
        batch.map((m) => api.hideChatMessage({ platform, account, id: m.id, scope, hidden })),
      );
      count += settled.filter((r) => r.status === "fulfilled").length;
      failed += settled.filter((r) => r.status === "rejected").length;
    }
    // A partial run is reported with both numbers, because the lines that did
    // get hidden are real and the moderator needs to know which ones.
    if (failed > 0) {
      throw new Error(
        t("chatCard.hidePartial", { done: count, total: messages.length, failed }),
      );
    }
    // The COUNT, not just the fact. "Hidden" over a loop that ran zero times
    // was the original defect; a number the moderator can check against what
    // they were looking at is what makes the report falsifiable.
    const one = count === 1;
    if (scope === "local") {
      return {
        detail: t(one ? "chatCard.hiddenLocalOne" : "chatCard.hiddenLocalMany", { count, platform }),
      };
    }
    return {
      detail: hidden
        ? t(one ? "chatCard.hiddenUpstreamOne" : "chatCard.hiddenUpstreamMany", {
            count,
            platform: accent.label,
          })
        : t(one ? "chatCard.shownUpstreamOne" : "chatCard.shownUpstreamMany", {
            count,
            platform: accent.label,
          }),
    };
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span style={card?.color ? { color: card.color } : undefined}>
              {card?.name || authorName}
            </span>
            {card?.broadcaster && <Badge variant="armed">{t("chatCard.broadcaster")}</Badge>}
            {card?.moderator && (
              <Badge variant="armed">
                <Shield className="mr-0.5 h-2.5 w-2.5" />
                {t("chatpage.moderator")}
              </Badge>
            )}
            {card?.subscriber && <Badge variant="outline">{t("chatCard.subscriber")}</Badge>}
            <Badge variant="outline" className={accent.text}>
              {accent.label}
            </Badge>
          </DialogTitle>
          {/* Keyed with the rest of this dialog's prose, so the paragraph is
              never half-translated. */}
          <DialogDescription>
            {loading
              ? t("chatCard.reading")
              : failed
                ? t("chatCard.readFailed")
                : t(messages.length === 1 ? "chatCard.onRecordOne" : "chatCard.onRecordMany", {
                    count: `${messages.length}${card?.truncated ? "+" : ""}`,
                  })}
          </DialogDescription>
        </DialogHeader>

        {/* ---- what they said ---- */}
        <div className="max-h-64 overflow-y-auto rounded border border-border bg-card-raised p-2">
          {loading && messages.length === 0 ? (
            <p className="p-2 text-[11px] text-muted-foreground">
              <Loader2 className="mr-1 inline h-3 w-3 animate-spin" />
              {t("common.loading")}
            </p>
          ) : failed ? (
            /* NOT the empty state. "Nothing from this person is in the
               scrollback" is a positive claim — it says the server answered
               and this person has said nothing — and a moderator reads it as a
               first offence. An unanswered question must not produce an
               answer, least of all the exonerating one. */
            <p className="p-2 text-[11px] text-warn">
              {t("chatCard.failedBody")}
            </p>
          ) : messages.length === 0 ? (
            <p className="p-2 text-[11px] text-muted-foreground">
              {t("chatCard.emptyBody")}
            </p>
          ) : (
            messages.map((m: ChatMessage) => (
              <div key={m.id} className="py-0.5 text-[12px] leading-snug">
                <span className="tnum mr-1.5 font-mono text-sm text-muted-foreground">
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
          <p className="text-sm leading-relaxed text-muted-foreground">
            {card.truncated && <strong>{t("chatpage.showingRecentOnly")}</strong>}
            {card.retentionNote}
          </p>
        )}

        {/* ---- what can be done ---- */}
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-sm uppercase tracking-wider text-muted-foreground">
              {t("automod.actionTimeout")}
            </span>
            {TIMEOUTS.map((to) => (
              <Button
                key={to.seconds}
                size="sm"
                variant="secondary"
                disabled={!ban.ok || busy !== ""}
                title={ban.reason || t("chatCard.timeOutFor", { duration: t(to.label) })}
                onClick={() =>
                  run(t("chatCard.timedOutFor", { duration: t(to.label) }), () =>
                    api.banChatUser({ platform, account, userId: authorId, seconds: to.seconds }),
                  )
                }
              >
                {busy === t("chatCard.timedOutFor", { duration: t(to.label) }) ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <Timer className="mr-1 h-3 w-3" />
                )}
                {t(to.label)}
              </Button>
            ))}
          </div>

          {/* Beside the buttons it governs, not at the foot of the dialog. One
              sentence covering every action was fine while one flag gated
              every action; now that Facebook can hide and delete but cannot
              ban, a card-wide "polyemesis cannot moderate Facebook" would be
              false about two of the controls on screen. */}
          {!ban.ok && <p className="text-sm text-warn">{ban.reason}</p>}

          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-sm uppercase tracking-wider text-muted-foreground">
              {t("secret.hide")}
            </span>

            {/* A CONTROL, and the WARNING beside it, because the two failures
                here are different.

                The control: this button loops over the messages the card is
                holding, so with none it hides nothing — and then reported
                "Hidden in polyemesis only. Everyone watching on twitch can
                still see them.", a sentence describing work that did not
                happen. A press with one possible outcome and a success toast
                is worse than a missing button: the moderator moves on
                believing the line is gone from their own overlay.

                The warning: `disabled` alone would be a grey button with no
                reason, which is the mute-control pattern this audit is full
                of. The sentence below says which of the two cases it is —
                nothing on record, or nothing readable. */}
            <Button
              size="sm"
              variant="secondary"
              disabled={busy !== "" || nothingToHide !== ""}
              title={
                nothingToHide ||
                t("chatCard.hideLocalTitle")
              }
              onClick={() => run(t("chatCard.hiddenHere"), hideAll("local", true))}
            >
              <EyeOff className="mr-1 h-3 w-3" />
              {t("chatCard.hideHereOnly")}
            </Button>

            {/* THE OTHER SCOPE, which no operator could reach.
                api.hideChatMessage has taken `scope: "local" | "platform"`
                from the day it was written, and the only caller in the
                codebase passed the literal "local". So Facebook's upstream
                hide — a comment taken off the public thread, reversibly,
                which is the one moderation power Facebook has that nothing
                else here does — existed end to end in the server and hub and
                could not be asked for.

                Rendered on every platform and inert with the reason on the
                four that cannot do it, rather than appearing only on
                Facebook: a control that comes and goes as you click between
                platforms teaches nobody what the platforms differ on. */}
            <Button
              size="sm"
              variant="secondary"
              disabled={!hideUpstream.ok || busy !== "" || nothingToHide !== ""}
              title={
                hideUpstream.reason ||
                nothingToHide ||
                t("chatCard.hideUpstreamTitle", { platform: accent.label })
              }
              onClick={() => run(t("chatCard.hiddenFromViewers"), hideAll("platform", true))}
            >
              {busy === t("chatCard.hiddenFromViewers") ? (
                <Loader2 className="mr-1 h-3 w-3 animate-spin" />
              ) : (
                <EyeOff className="mr-1 h-3 w-3" />
              )}
              {t("chatCard.hideFromViewers")}
            </Button>

            {/* THE UNDO, rendered rather than described. "Hiding is
                reversible" is a claim, and a moderator deciding between hide
                and delete under time pressure has no reason to believe a claim
                with no button behind it. This is that button. */}
            <Button
              size="sm"
              variant="ghost"
              disabled={!hideUpstream.ok || busy !== "" || nothingToHide !== ""}
              title={
                hideUpstream.reason ||
                nothingToHide ||
                t("chatCard.showAgainTitle", { platform: accent.label })
              }
              onClick={() => run(t("chatCard.shownAgain"), hideAll("platform", false))}
            >
              <Eye className="mr-1 h-3 w-3" />
              {t("chatCard.showToViewersAgain")}
            </Button>

            {nothingToHide !== "" && !loading && (
              <span className="text-sm text-muted-foreground">
                {failed
                  ? t("chatCard.nothingHereFailed")
                  : t("chatCard.nothingHereEmpty")}
              </span>
            )}
          </div>

          {/* THE DIFFERENCE, in the operator's terms, beside the buttons that
              make it. Hiding and deleting are one endpoint apart in the API
              and a world apart to the person pressing them, and the pane
              offers no way to find that out at the moment of the decision. */}
          <p className="text-sm leading-relaxed text-muted-foreground">
            {t("chatCard.undoNote")} {t("chatCard.localHideNote")}{" "}
            {hideUpstream.ok
              ? t("chatCard.upstreamNote", { platform: accent.label })
              : hideUpstream.reason}
          </p>

          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-sm uppercase tracking-wider text-muted-foreground">
              {t("automod.actionBan")}
            </span>

            <Button
              size="sm"
              variant="secondary"
              disabled={!ban.ok || busy !== ""}
              title={ban.reason || t("chatCard.liftBanTitle")}
              onClick={() =>
                run(t("chatCard.banLifted"), () =>
                  api.unbanChatUser({ platform, account, userId: authorId }),
                )
              }
            >
              <Undo2 className="mr-1 h-3 w-3" />
              {t("chatCard.liftBan")}
            </Button>

            {/* Last, and asks again. The only irreversible action here. */}
            {confirmBan ? (
              <>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={busy !== ""}
                  onClick={() =>
                    run(t("chatCard.banned"), async () => {
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
                  {busy === t("chatCard.banned") ? (
                    <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                  ) : (
                    <Ban className="mr-1 h-3 w-3" />
                  )}
                  {t("chatCard.banConfirm")}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setConfirmBan(false)}>
                  {t("common.cancel")}
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="outline"
                disabled={!ban.ok || busy !== ""}
                className={cn(ban.ok && "text-down hover:text-down")}
                title={ban.reason || t("chatCard.banAsksAgain")}
                onClick={() => setConfirmBan(true)}
              >
                <Ban className="mr-1 h-3 w-3" />
                {t("chatCard.banPermanently")}
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
