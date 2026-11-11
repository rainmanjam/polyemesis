import { describe, expect, it } from "vitest";
import { STORIES, STORY_ORDER, destLabel, statusFor } from "./stories";

describe("story data", () => {
  it("has three stories in tab order", () => expect(STORY_ORDER).toEqual(["streamer", "sports", "worship"]));
  it.each(STORY_ORDER)("%s has 4 tracks, 4 unique destinations, one archive, gains in {0,.5,1}", (k) => {
    const s = STORIES[k];
    expect(s.tracks).toHaveLength(4);
    expect(new Set(s.dests.map((d) => d.id)).size).toBe(s.dests.length);
    expect(s.dests.filter((d) => d.archive)).toHaveLength(1);
    for (const d of s.dests) { expect(d.mix).toHaveLength(4); for (const g of d.mix) expect([0, 0.5, 1]).toContain(g); }
    expect(s.caption && s.hint && s.fine).toBeTruthy();
  });
  it("gives worship exactly one feed with no video", () =>
    expect(STORIES.worship.dests.filter((d) => !d.video).map((d) => d.id)).toEqual(["audio"]));
  it("puts the licence line in every fine print", () => {
    for (const k of STORY_ORDER) expect(STORIES[k].fine).toMatch(/doesn't license/);
  });
  it("keeps the rights clause in every fine print (F16)", () => {
    for (const k of STORY_ORDER) expect(STORIES[k].fine).toContain("Use tracks you have the rights to.");
  });
  it("adds the translation sentence to sports and worship only", () => {
    expect(STORIES.streamer.fine).not.toMatch(/Translation/);
    for (const k of ["sports", "worship"] as const) expect(STORIES[k].fine).toMatch(/^Translation comes from your own interpreters/);
  });
  it("states the translation sentence as the owner approved it", () => {
    for (const k of ["sports", "worship"] as const)
      expect(STORIES[k].fine).toContain(
        "Translation comes from your own interpreters or translation service, as a track you send; polyemesis routes each language.",
      );
  });
  it("is mutable enough for a test to change a name and restore it (F23)", () => {
    const d = STORIES.streamer.dests[0];
    const old = d.name;
    try {
      d.name = "X";
      expect(statusFor(STORIES.streamer, d.id, false).name).toBe("X");
    } finally {
      d.name = old;
    }
    expect(statusFor(STORIES.streamer, d.id, false).name).toBe(old);
  });
});
describe("statusFor", () => {
  const s = STORIES.streamer;
  it("returns the caption, with the hint only when interactive", () => {
    expect(statusFor(s, null, false)).toEqual({ name: null, text: s.caption });
    expect(statusFor(s, null, true).text).toBe(`${s.caption} ${s.hint}`);
  });
  it("names what YouTube carries and what stays off", () =>
    expect(statusFor(s, "youtube", true)).toEqual({ name: "YouTube", text: "carries mic, game and co-host. Music stays off. The video is the same, untouched." }));
  it("marks a lowered track", () =>
    expect(statusFor(s, "kick", true).text).toBe("carries mic, music (turned down), game and co-host. The video is the same, untouched."));
  it("says when a feed has no video", () =>
    expect(statusFor(STORIES.worship, "audio", true)).toEqual({ name: "Audio-only feed", text: "carries the room, English and music. Spanish stays off. No video goes to this feed." }));
  it("uses plural 'stay off' for several silent tracks", () =>
    expect(statusFor(STORIES.sports, "home", true).text).toBe("carries stadium ambience and home commentary. Away commentary and Spanish commentary stay off. The video is the same, untouched."));
  it("falls back to the caption for an unknown id", () => expect(statusFor(s, "nope", false).name).toBeNull());
});
describe("destLabel", () => {
  it("is a full sentence", () =>
    expect(destLabel(STORIES.streamer, STORIES.streamer.dests[1])).toBe(
      "YouTube: No music. Carries mic, game and co-host, plus the untouched video. Press to solo this mix."));
  it("says so when a feed has no video", () =>
    expect(destLabel(STORIES.worship, STORIES.worship.dests[2])).toBe(
      "Audio-only feed: English, no video. Carries the room, English and music, with no video. Press to solo this mix."));
});
