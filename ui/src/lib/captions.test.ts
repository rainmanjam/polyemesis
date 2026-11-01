import { describe, expect, it } from "vitest";

import { captionModeNeedsDelay, captionModesFor, captionModeToSend, captionsOffered } from "@/lib/captions";

// The console's caption picker reads these. The server decides what is accepted;
// these tests pin what the picker offers, so an operator cannot pick a route the
// server would refuse.

describe("caption routes offered per platform", () => {
  it("offers only off for a platform with no route, so a new platform is never captioned by accident", () => {
    expect(captionModesFor("trovo")).toEqual(["off"]);
    expect(captionModesFor("")).toEqual(["off"]);
  });

  it("offers Kick nothing at all, and says so through captionsOffered", () => {
    expect(captionModesFor("kick")).toEqual(["off"]);
    expect(captionsOffered("kick")).toBe(false);
  });

  it("offers Twitch the in-stream route only", () => {
    expect(captionModesFor("twitch")).toEqual(["off", "sei"]);
    expect(captionsOffered("twitch")).toBe(true);
  });

  it("offers YouTube both the caption POST and the embedded route, the POST first in the table", () => {
    expect(captionModesFor("youtube")).toEqual(["off", "youtube_post", "sei"]);
  });

  it("offers Facebook its provisional route, which the server also gates behind a switch", () => {
    expect(captionModesFor("facebook")).toEqual(["off", "facebook_sei"]);
  });

  it("asks for a broadcast delay only for the YouTube caption POST", () => {
    expect(captionModeNeedsDelay("youtube_post")).toBe(true);
    expect(captionModeNeedsDelay("sei")).toBe(false);
    expect(captionModeNeedsDelay("off")).toBe(false);
  });
});

describe("the caption mode a save sends", () => {
  // MUTATION: captionModeToSend returns mode unconditionally. Observed: the
  // switch-to-Kick row sends "sei" and the test fails.
  it("sends off when the platform is switched to Kick, which offers no route", () => {
    expect(captionModeToSend("rtmp", "kick", "sei")).toBe("off");
  });

  it("sends off when the kind is switched away from RTMP", () => {
    expect(captionModeToSend("srt", "twitch", "sei")).toBe("off");
  });

  it("keeps a mode the platform still offers", () => {
    expect(captionModeToSend("rtmp", "twitch", "sei")).toBe("sei");
    expect(captionModeToSend("rtmp", "youtube", "youtube_post")).toBe("youtube_post");
  });

  it("sends a mode the new platform does not offer as off, rather than the stored one", () => {
    expect(captionModeToSend("rtmp", "twitch", "youtube_post")).toBe("off");
  });
});
