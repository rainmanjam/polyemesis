import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { MAX_DUMP_SECONDS, VIEWER_PLATFORMS } from "../src/config.js";
import { destinationFace } from "../src/keyFace.js";
import {
  ACCOUNT_HEALTH_STATES,
  FAILURE_CAUSES,
  PROCESS_STATES,
  RECORDING_STATE_WORDS,
  SOURCE_KINDS,
  type DestStatus,
} from "../src/model.js";
import wire from "./contract/wire.json" with { type: "json" };

// The plugin restates words and limits the server owns. contract/wire.json is
// written by the Go test TestWireContract (internal/api) from the server's own
// constants, so each list here is held to the server's list in BOTH directions:
// a word the server adds must reach the plugin, and a word the plugin keeps
// that the server no longer sends is dead.

const sorted = (xs: readonly string[]) => [...xs].sort();

describe("the plugin's wire words are the server's", () => {
  it("process states", () => {
    expect(sorted(PROCESS_STATES)).toEqual(sorted(wire.processStates));
  });

  it("failure causes", () => {
    expect(sorted(FAILURE_CAUSES)).toEqual(sorted(wire.failureCauses));
  });

  it("every cause has its own key label, and only unknown may fall back", () => {
    const dest = (cause: string): DestStatus =>
      ({
        id: 1,
        name: "YouTube",
        platform: "youtube",
        enabled: true,
        sourceId: 7,
        process: { state: "failed" },
        reason: { cause },
      }) as unknown as DestStatus;
    for (const cause of wire.failureCauses) {
      expect(destinationFace(dest(cause), undefined, null).reason, cause).toBe(`streamdeck.reason.${cause}`);
    }
  });

  it("account health states, minus the healthy one the server leaves out", () => {
    expect(sorted(ACCOUNT_HEALTH_STATES)).toEqual(sorted(wire.accountHealthStates.filter((s) => s !== "ok")));
  });

  it("failover source kinds", () => {
    expect(sorted(SOURCE_KINDS)).toEqual(sorted(wire.sourceKinds));
  });

  it("recording state words", () => {
    expect(sorted(RECORDING_STATE_WORDS)).toEqual(sorted(wire.recordingStates));
  });
});

describe("the plugin's limits are the server's", () => {
  it("the longest delay dump", () => {
    expect(MAX_DUMP_SECONDS).toBe(wire.limits.maxDumpSeconds);
  });

  it("the platforms a viewers key may show", () => {
    expect(sorted(VIEWER_PLATFORMS)).toEqual(sorted(wire.viewerPlatforms));
  });
});

describe("the property inspector's copies are the server's", () => {
  const html = readFileSync(
    new URL("../com.polyemesis.streamdeck.sdPlugin/ui/pi.html", import.meta.url),
    "utf8",
  );

  it("offers exactly the viewer platforms, plus the total", () => {
    const select = /<select id="viewersPlatform">([\s\S]*?)<\/select>/.exec(html)?.[1] ?? "";
    const values = [...select.matchAll(/<option value="([^"]*)"/g)].map((m) => m[1] as string);
    expect(sorted(values)).toEqual(sorted(["", ...wire.viewerPlatforms]));
  });

  it("states the dump ceiling the server enforces", () => {
    expect(html).toContain(`whole 1 to ${wire.limits.maxDumpSeconds}`);
  });
});
