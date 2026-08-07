import { describe, expect, it } from "vitest";

import { platformLinkFor, platformNoun } from "./platformLinks";
import type { ChatMessage, ChatPlatform } from "./types";

function msg(over: Partial<ChatMessage> & { platform: ChatPlatform }): ChatMessage {
  return {
    id: "m1",
    text: "hello",
    at: "2026-08-05T12:00:00Z",
    ...over,
    // After the spread: the author default has to merge with any override
    // rather than be replaced by it, and `platform` is required.
    platform: over.platform,
    author: { id: "u1", name: "Viewer", ...(over.author ?? {}) },
  } as ChatMessage;
}

describe("platformLinkFor", () => {
  it("builds Twitch's moderator viewer card when the channel is known", () => {
    const link = platformLinkFor(
      msg({ platform: "twitch", channel: "SomeStreamer", author: { id: "u1", name: "BadActor" } }),
    );
    // The viewer card is channel-scoped: both halves of the path matter, and
    // both are lowercased because Twitch logins are.
    expect(link?.url).toBe("https://www.twitch.tv/popout/somestreamer/viewercard/badactor");
    expect(link?.kind).toBe("moderator-card");
  });

  // The distinction the whole module exists for. Only Twitch-with-a-channel is
  // a moderation view; if anything else ever reports "moderator-card" the UI
  // promises a moderator history and hands them a profile page.
  it("marks every non-Twitch platform as a profile, not a moderator card", () => {
    for (const platform of ["youtube", "kick", "facebook"] as const) {
      const link = platformLinkFor(msg({ platform, channel: "chan" }));
      expect(link, platform).not.toBeNull();
      expect(link?.kind, platform).toBe("profile");
      // And each must say why, or the caveat is decoration.
      expect(link?.caveat, platform).not.toBe("");
    }
  });

  it("falls back to the Twitch channel page when no channel came with the message", () => {
    const link = platformLinkFor(msg({ platform: "twitch", author: { id: "u1", name: "BadActor" } }));
    expect(link?.url).toBe("https://www.twitch.tv/badactor");
    // Downgraded honestly rather than guessing a channel to keep the richer label.
    expect(link?.kind).toBe("profile");
  });

  it("uses the channel id for YouTube, not the display name", () => {
    // Display names collide and change; the author id is the channel id and is
    // the only thing that resolves to the right person.
    const link = platformLinkFor(
      msg({ platform: "youtube", author: { id: "UC123abc", name: "Some Name" } }),
    );
    expect(link?.url).toBe("https://www.youtube.com/channel/UC123abc");
  });

  it("returns null rather than a wrong link when the identifier is missing", () => {
    // A link built from nothing lands on the platform's home page or a 404, and
    // a moderator who clicks it concludes the feature is broken. No menu item
    // is the better outcome.
    expect(platformLinkFor(msg({ platform: "youtube", author: { id: "", name: "Anon" } }))).toBeNull();
    expect(platformLinkFor(msg({ platform: "facebook", author: { id: "", name: "Anon" } }))).toBeNull();
    expect(platformLinkFor(msg({ platform: "kick", author: { id: "u1", name: "" } }))).toBeNull();
    expect(platformLinkFor(msg({ platform: "twitch", author: { id: "u1", name: "" } }))).toBeNull();
  });

  it("has no link shape to guess for a custom platform", () => {
    expect(platformLinkFor(msg({ platform: "custom" }))).toBeNull();
  });

  // Names arrive from strangers on the internet and go straight into a URL.
  it("escapes names so a crafted one cannot reshape the URL", () => {
    const link = platformLinkFor(
      msg({ platform: "kick", author: { id: "u1", name: "evil/../../admin?x=1" } }),
    );
    expect(link?.url.startsWith("https://kick.com/")).toBe(true);
    // Path separators and query markers must be percent-encoded, or the link
    // navigates somewhere other than the profile it claims to open.
    expect(link?.url).not.toContain("/../");
    expect(link?.url).not.toContain("?");
  });

  it("strips a leading @ that viewers often include in names", () => {
    const link = platformLinkFor(msg({ platform: "kick", author: { id: "u1", name: "@Viewer" } }));
    expect(link?.url).toBe("https://kick.com/viewer");
  });
});

describe("platformNoun", () => {
  it("uses each platform's own capitalisation", () => {
    expect(platformNoun("youtube")).toBe("YouTube");
    expect(platformNoun("twitch")).toBe("Twitch");
    expect(platformNoun("kick")).toBe("Kick");
    expect(platformNoun("facebook")).toBe("Facebook");
  });
});
