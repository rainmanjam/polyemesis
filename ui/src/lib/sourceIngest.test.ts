import { describe, expect, it } from "vitest";
import { ingestPhase, ingestSummary, srtPortOf } from "./sourceIngest";
import type { SourceView } from "./types";

// The three things a source card can honestly say about ingest, and the one
// it must not say. A card that reads "waiting" while an encoder is streaming
// sends the operator to reconfigure a working feed; a card that reads
// "receiving" with nothing connected is the false assurance this page exists
// to prevent.

const base = {
  enabled: true,
  ingest: { mode: "srt" },
  publishing: false,
  link: undefined,
  rtmpLink: undefined,
  publishUrls: { srt: "srt://<server>:40557?streamid=studio-a" },
} as unknown as Pick<
  SourceView,
  "enabled" | "ingest" | "publishing" | "link" | "rtmpLink" | "publishUrls"
>;

describe("ingestPhase", () => {
  it("is unset until a protocol is chosen, so no waiting state claims a feed that cannot exist", () => {
    expect(ingestPhase({ ...base, ingest: { mode: undefined } as never })).toBe("unset");
  });

  it("is off for a disabled source, whatever the encoder is doing", () => {
    expect(ingestPhase({ ...base, enabled: false, publishing: true })).toBe("off");
  });

  it("is waiting for an SRT or RTMP source with no encoder yet", () => {
    expect(ingestPhase(base)).toBe("waiting");
    expect(ingestPhase({ ...base, ingest: { mode: "rtmp" } as never })).toBe("waiting");
  });

  it("is receiving once the shared listener reports a live publisher", () => {
    expect(ingestPhase({ ...base, publishing: true })).toBe("receiving");
  });

  it("is receiving when an uplink is attributed to this source, even off the shared listener", () => {
    // The shared-listener flag is false for a per-port source that IS live;
    // the uplink is the signal that survives that. Treating `publishing` as the
    // only truth would report a streaming encoder as waiting.
    const link = { peer: "10.0.0.5", rttMs: 0.3, lossPackets: 0, retransPackets: 0 } as never;
    expect(ingestPhase({ ...base, link })).toBe("receiving");
  });

  it("is never waiting for pull ingest, which has its own fields and no encoder to point", () => {
    expect(ingestPhase({ ...base, ingest: { mode: "pull" } as never })).toBe("unset");
  });
});

describe("srtPortOf", () => {
  it("reads the port out of the server's SRT publish address", () => {
    expect(srtPortOf({ srt: "srt://<server>:40557?streamid=x" })).toBe(40557);
  });

  it("is null when there is no SRT address yet", () => {
    expect(srtPortOf({})).toBeNull();
    expect(srtPortOf({ rtmp: "rtmp://<server>:1935/live" })).toBeNull();
  });
});

describe("ingestSummary", () => {
  it("shows the protocol and port as a value, not an empty select", () => {
    expect(ingestSummary(base)).toEqual({ proto: "SRT", detail: ":40557" });
  });

  it("names RTMP without inventing a port", () => {
    expect(ingestSummary({ ...base, ingest: { mode: "rtmp" } as never })).toEqual({
      proto: "RTMP",
      detail: null,
    });
  });

  it("is null while the protocol is unset, so the placeholder shows", () => {
    expect(ingestSummary({ ...base, ingest: { mode: undefined } as never })).toBeNull();
  });
});
