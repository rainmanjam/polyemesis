import type { ChatMessage, ChatPlatform } from "@/lib/types";

// Where a viewer can be looked at on the platform they were talking on.
//
// The honest summary, which the UI must not blur: only ONE of these four is a
// chat-history view. Twitch's popout viewer card is the window a Twitch
// moderator already knows — recent messages in this channel, plus the ban and
// timeout controls. The other three have no equivalent at any URL, so the best
// available link is the person's profile or channel, which shows what they
// publish and nothing about what they said in chat.
//
// That difference is the whole reason `kind` exists. A single "Open on
// <platform>" label would promise a moderator the same thing everywhere and
// deliver it only on Twitch, and the failure is silent: they click, get a
// profile page, and conclude the viewer has no history worth acting on.
//
// polyemesis's own user card is the cross-platform answer and stays the primary
// one; these links are the escape hatch for acting where the platform's own
// tools are richer. See internal/db/chat.go's ChatMessagesByAuthor for why no
// platform API can close this gap.

export type PlatformLinkKind =
  /** The platform's own moderation view of this person, with history. */
  | "moderator-card"
  /** A profile or channel page. No chat history, no moderation controls. */
  | "profile";

export interface PlatformLink {
  url: string;
  kind: PlatformLinkKind;
  /** Menu label. Says what the destination actually is. */
  label: string;
  /** The caveat, for a title attribute. Empty when there is nothing to warn about. */
  caveat: string;
}

/** Twitch display names are the login with capitalisation for most accounts,
 *  but an account with a localised display name (common on CJK channels) has a
 *  login that shares none of its characters. We store the display name, so the
 *  viewer card URL is right for the common case and wrong for that minority —
 *  which is why the caveat below says the name may not resolve rather than
 *  claiming the link always lands. */
function twitchLogin(name: string): string {
  return name.trim().toLowerCase().replace(/^@/, "");
}

/** The platform link for whoever sent this message, or null when we cannot
 *  build one that would land somewhere useful. */
export function platformLinkFor(m: ChatMessage): PlatformLink | null {
  const id = m.author.id?.trim() ?? "";
  const name = m.author.name?.trim() ?? "";

  switch (m.platform) {
    case "twitch": {
      // The viewer card is scoped to a channel: it shows what this person said
      // in THAT chat. Without the channel there is no card to open, so fall
      // back to the profile rather than guessing a channel.
      const channel = m.channel?.trim();
      const login = twitchLogin(name);
      if (!login) return null;
      if (channel) {
        return {
          url: `https://www.twitch.tv/popout/${encodeURIComponent(
            twitchLogin(channel),
          )}/viewercard/${encodeURIComponent(login)}`,
          kind: "moderator-card",
          label: "Open moderator card on Twitch",
          caveat:
            "Twitch's own viewer card for this channel: their recent messages here, plus ban and timeout. " +
            "Opens logged in as whoever this browser is signed in to Twitch as. " +
            "An account with a localised display name may not resolve.",
        };
      }
      return {
        url: `https://www.twitch.tv/${encodeURIComponent(login)}`,
        kind: "profile",
        label: "Open channel on Twitch",
        caveat:
          "Their channel page, not a moderator view — this message arrived without a channel, " +
          "so there is no viewer card to open.",
      };
    }

    case "youtube": {
      // YouTube author ids ARE channel ids, so this lands on the right person
      // even when the display name is ambiguous or has changed.
      if (!id) return null;
      return {
        url: `https://www.youtube.com/channel/${encodeURIComponent(id)}`,
        kind: "profile",
        label: "Open channel on YouTube",
        caveat:
          "YouTube publishes no per-viewer chat history, so this is their channel page. " +
          "Moderate from the live chat in YouTube Studio.",
      };
    }

    case "kick": {
      if (!name) return null;
      return {
        url: `https://kick.com/${encodeURIComponent(twitchLogin(name))}`,
        kind: "profile",
        label: "Open profile on Kick",
        caveat:
          "Kick publishes no per-viewer chat history, so this is their profile page.",
      };
    }

    case "facebook": {
      // Facebook comment author ids are page-scoped: the id identifies the
      // person only within the page they commented on, so this resolves for the
      // page owner and can 404 for anyone else.
      if (!id) return null;
      return {
        url: `https://www.facebook.com/${encodeURIComponent(id)}`,
        kind: "profile",
        label: "Open profile on Facebook",
        caveat:
          "Facebook publishes no per-viewer chat history, so this is their profile. " +
          "Comment author ids are scoped to the page, so this may not resolve from another account.",
      };
    }

    default:
      // A custom or future platform: no URL shape we can honestly guess at.
      return null;
  }
}

/** Opens a platform link in a real separate window rather than a tab, because
 *  the point is to review there while still watching chat here.
 *
 *  `noopener` is not optional: without it the opened page gets a handle on this
 *  one through `window.opener` and can navigate it somewhere else, which on an
 *  operator console that is already authenticated is worth refusing outright. */
export function openPlatformLink(link: PlatformLink): void {
  window.open(link.url, `polyemesis-${link.kind}`, "noopener,noreferrer,width=420,height=720");
}

/** Human label for a platform, for menu copy. */
export function platformNoun(p: ChatPlatform): string {
  switch (p) {
    case "twitch":
      return "Twitch";
    case "youtube":
      return "YouTube";
    case "kick":
      return "Kick";
    case "facebook":
      return "Facebook";
    default:
      return p;
  }
}
