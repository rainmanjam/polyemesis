import { describe, expect, it } from "vitest";

import { presetIdFor } from "./DestinationDialog";
import type { Destination } from "@/lib/types";

const dest = (url: string, platform = "twitch") => ({ url, platform }) as unknown as Destination;

/* A stored Twitch URL names its preset by scheme and host, as the server's
 * inference does, so a port or path difference does not flip the label. */
describe("presetIdFor", () => {
  it.each([
    ["rtmp://live.twitch.tv/app", "twitch"],
    ["rtmps://live.twitch.tv:443/app", "twitch-rtmps"],
    ["rtmps://live.twitch.tv/app", "twitch-rtmps"],
    ["RTMPS://Live.Twitch.TV/app", "twitch-rtmps"],
  ])("labels %s as %s", (url, id) => {
    expect(presetIdFor(dest(url))).toBe(id);
  });

  it("falls back to the platform's first preset for an unknown host", () => {
    expect(presetIdFor(dest("rtmp://ingest.example/app"))).toBe("twitch");
  });

  it("labels nothing for a custom destination on an unknown URL", () => {
    expect(presetIdFor(dest("rtmp://ingest.example/app", "custom"))).toBe("");
  });
});
