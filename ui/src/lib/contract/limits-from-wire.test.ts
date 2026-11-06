import { describe, expect, it } from "vitest";
import { WIRE } from "./wire";
import { OVERLAY_FONTS, OVERLAY_LIMITS } from "../overlay";
import { POLL_LIMITS } from "../polls";
import { outputDelaySeconds } from "../captions";

/* The poll and overlay limits, the caption-delay exception and the CSRF names
 * are the server's (internal/polls, internal/overlay, db.OutputDelaySeconds,
 * internal/auth), generated into the wire contract. These hold the console's
 * copies to it, so a value cannot be typed here and drift. */
describe("values the console reads from the wire contract", () => {
  it("takes the poll limits from it", () => {
    expect(POLL_LIMITS).toEqual({
      minOptions: WIRE.polls.minOptions,
      maxOptions: WIRE.polls.maxOptions,
      maxQuestion: WIRE.polls.maxQuestion,
      maxOption: WIRE.polls.maxOption,
      minDuration: WIRE.polls.minDurationSeconds,
      maxDuration: WIRE.polls.maxDurationSeconds,
    });
  });

  it("takes the overlay fonts and bounds from it", () => {
    expect(OVERLAY_FONTS).toEqual(WIRE.overlay.fonts);
    expect(OVERLAY_LIMITS.fontSize).toEqual(WIRE.overlay.fontSize);
    expect(OVERLAY_LIMITS.fadeSeconds).toEqual(WIRE.overlay.fadeSeconds);
    expect(OVERLAY_LIMITS.maxLines).toEqual(WIRE.overlay.maxLines);
    expect(OVERLAY_LIMITS.backgroundOpacity).toEqual(WIRE.overlay.backgroundOpacity);
  });

  it("holds the output back by 0 only for the contract's platform and caption mode", () => {
    const { noLocalDelayPlatform: platform, noLocalDelayMode: mode } = WIRE.captions;
    expect(outputDelaySeconds(platform, mode as never, 30)).toBe(0);
    expect(outputDelaySeconds(platform, "off", 30)).toBe(30);
    expect(outputDelaySeconds("twitch", mode as never, 30)).toBe(30);
  });
});
