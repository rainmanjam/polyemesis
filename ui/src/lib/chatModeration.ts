import { capabilityFor, supportOf } from "@/lib/capabilities";
import { translate, type Translator } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";
import type { ChatMessage, ChatPlatform } from "@/lib/types";

/* ===========================================================================
   Which moderation action can a platform actually perform.

   PER ACTION, not per column. The capability matrix has ONE `moderation` cell
   per platform and the chat UI was gating every control on it:

       supportOf(capabilityFor(platform), "moderation") === "yes"

   That cell is a summary written for the setup page — "can polyemesis moderate
   here at all" — and summarising is exactly what it must not be asked to do at
   a button. Facebook answers "yes" to the column because it can delete and
   hide a comment, and its adapter implements no Banner at all: FacebookAdapter
   has Delete, Hide, Run and Health, and Hub.Ban type-asserts chat.Banner and
   refuses (internal/chat/hub.go:803). So "Time out 1 min" and "Ban
   permanently" rendered live and enabled on Facebook, and every press produced
   an error toast. The mirror image was Rumble: the menu rendered Delete on
   `{onDelete && …}` with no gate whatever, three lines below its own sentence
   saying Rumble publishes no moderation API.

   A control that is enabled and always fails is worse than a missing one. The
   moderator presses it mid-raid, reads a toast, presses it again, and the line
   they were trying to remove is still on the overlay. This module is the gate
   that makes that impossible to express: ask about the ACTION, get a yes or a
   sentence you can put on screen.

   The shape is deliberately the one the automod matrix already uses.
   internal/automod/capabilities.go answers Can(platform, action) (bool,
   string), AutomodCell carries {available, reason}, and AutomodMatrix.tsx
   renders an unavailable cell inert WITH the reason rather than as an unticked
   box. Same rule, same reasoning, same words where the words already exist —
   this is the client-side half of that table for the actions a human takes by
   hand.

   Mirrors what internal/chat's adapters implement, and chatModeration.drift
   .test.ts reads chat.go's interface assertions and fails if the two disagree.
   Two tables describing the same four platforms is precisely the drift this
   repo writes guards for elsewhere, and here the failure mode is a moderator
   believing a channel is under control when nothing is wired to it.
   =========================================================================== */

/** What a moderator can ask for, one action at a time.
 *
 *  Split finer than the server's route table on purpose: `hideLocal` and
 *  `hideUpstream` are one endpoint with a `scope` parameter, and they are
 *  nothing alike to the person pressing them. One is reversible and invisible
 *  to viewers; the other changes what the audience sees. */
export type ChatModerationAction =
  | "delete"
  | "hideUpstream"
  | "hideLocal"
  | "timeout"
  | "ban";

/** A yes, or a sentence saying why not.
 *
 *  Never a bare boolean. A bare boolean is how a control ends up greyed with no
 *  explanation, which reads as a broken tool rather than as an unsupported
 *  platform — the same complaint ChatMessageMenu's own header has always
 *  made. `reason` is empty exactly when `ok` is true. */
export interface ChatActionSupport {
  ok: boolean;
  reason: string;
}

const YES: ChatActionSupport = { ok: true, reason: "" };

/** The reasons are sentences for the screen, so callers in a component pass
 *  their `t`. The English default is for callers with no language to hand —
 *  the drift test among them. */
const english: Translator = (key, params) => translate("en", key, params);

/** What each platform's chat adapter actually implements.
 *
 *  Keyed by the raw platform string rather than ChatPlatform because a message
 *  can carry a platform value this build has no row for (a platform newer than
 *  the UI, or a retired one such as Trovo), and a lookup on `string` answers
 *  "nothing implemented" for it instead of the type system promising it cannot
 *  occur. Rumble is a ChatPlatform with an adapter that implements no moderation
 *  API; its row says so, which is how an ungated Delete button once survived
 *  review.
 *
 *  `hideUpstream` is Facebook-only for a structural reason rather than a
 *  missing feature: Facebook's live chat is a comment thread, and a comment has
 *  an is_hidden field. Nothing else here has anywhere to hide a message to. */
const ADAPTERS: Record<
  string,
  { delete: boolean; ban: boolean; hideUpstream: boolean }
> = {
  youtube: { delete: true, ban: true, hideUpstream: false },
  twitch: { delete: true, ban: true, hideUpstream: false },
  kick: { delete: true, ban: true, hideUpstream: false },
  // XAdapter implements Deleter and Banner (a mute; a zero duration is
  // indefinite). Built from X's documentation and unverified against a live
  // account, which the capability matrix says; nothing here hides, because X
  // has no hide action.
  x: { delete: true, ban: true, hideUpstream: false },
  // No Banner. Not an oversight and not a scope we could request: Facebook
  // publishes no chat ban API at all.
  facebook: { delete: true, ban: false, hideUpstream: true },
};

/** The platform's name as an operator would write it.
 *
 *  platformName covers the four with adapters and returns its argument
 *  unchanged for everything else, which is how the menu came to print the
 *  lowercase "rumble publishes no moderation API". The capability matrix knows
 *  the proper name, so fall through to it before giving up. */
function noun(platform: string): string {
  const known = platformName(platform as ChatPlatform);
  if (known !== platform) return known;
  return capabilityFor(platform).name;
}

/**
 * Can this platform perform this action, and if not, what does the operator
 * need to read.
 *
 * Takes the platform as a plain string so a value outside ChatPlatform — see
 * ADAPTERS above — is answered rather than crashing or silently passing.
 */
export function chatActionSupport(
  platform: string,
  action: ChatModerationAction,
  t: Translator = english,
): ChatActionSupport {
  // Local hide touches no platform API. Hub.HideLocally deliberately works
  // whether or not an adapter is attached, because a disconnected platform's
  // messages are still on the operator's screen and being unable to clear them
  // because a socket dropped would be the wrong answer. So it is the one action
  // that can never be unsupported, and it is what the reasons below point at.
  if (action === "hideLocal") return YES;

  const row = ADAPTERS[platform];
  if (!row) {
    // No adapter capable of anything. Keeps the sentence the menu has always
    // printed for this case, so the existing wording survives.
    return {
      ok: false,
      reason: t("chatMod.noApi", { platform: noun(platform) }),
    };
  }

  switch (action) {
    case "delete":
      return row.delete
        ? YES
        : {
            ok: false,
            reason: t("chatMod.noDelete", { platform: noun(platform) }),
          };

    case "hideUpstream":
      return row.hideUpstream
        ? YES
        : {
            ok: false,
            // Mirrors internal/automod/capabilities.go's ActionHide reason.
            reason: t("chatMod.noHideUpstream", { platform: noun(platform) }),
          };

    case "timeout":
    case "ban":
      // Both route through Hub.Ban, which type-asserts chat.Banner, so they
      // stand or fall together. Answering them separately would invite a
      // caller to gate one and not the other.
      return row.ban
        ? YES
        : {
            ok: false,
            // Mirrors internal/automod/capabilities.go's ActionBan reason, so
            // an operator meeting this in the chat pane and in the automod
            // matrix reads the same sentence twice rather than two half-answers.
            reason: t("chatMod.noBan", { platform: noun(platform) }),
          };
  }
}

/** Why nobody can be addressed on THIS message, or "" when someone can.
 *
 *  A separate question from what the platform supports, and the operator has to
 *  be able to tell the two apart: "Facebook cannot time anyone out" and "this
 *  particular line arrived without a user id" call for completely different
 *  next steps. Actions against a PERSON need it; deleting a message does not,
 *  which is why Facebook can still delete a comment whose author id never
 *  arrived. */
export function authorReason(m: ChatMessage, t: Translator = english): string {
  const id = m.author.id?.trim() ?? "";
  if (id !== "") return "";
  return t("chatMod.noAuthorId", { platform: noun(m.platform) });
}

/** The support answer for an action against the person who sent a message.
 *
 *  Folds the two questions in the order the operator would ask them: is there
 *  anybody to act on, and can this platform act on them. */
export function personActionSupport(
  m: ChatMessage,
  action: ChatModerationAction,
  t: Translator = english,
): ChatActionSupport {
  const missing = authorReason(m, t);
  if (missing !== "") return { ok: false, reason: missing };
  return chatActionSupport(m.platform, action, t);
}

/** True when the platform's capability COLUMN claims moderation works.
 *
 *  Exported only so a test can pin the gap this module exists to close: a
 *  platform can answer "yes" here and "no" to an action, and every control in
 *  the chat pane used to be gated on this value alone. Nothing renders from it.
 */
export function moderationColumnSaysYes(platform: string): boolean {
  return supportOf(capabilityFor(platform), "moderation") === "yes";
}
