import { describe, expect, it } from "vitest";
import { classifyHlsError, classifyMediaError, MAX_STARTUP_MS, newSessionId, QoETracker } from "./qoeBeacon";
import type { BeaconV1 } from "@/lib/types";

function tracker() {
  let now = 1000;
  const sent: BeaconV1[] = [];
  const q = new QoETracker({ send: (b) => sent.push(b), now: () => now, sid: "0123456789abcdef0123456789abcdef" });
  return { q, sent, advance: (ms: number) => (now += ms) };
}

describe("QoETracker", () => {
  it("measures startup from creation to first frame and reports it on every beacon", () => {
    const { q, sent, advance } = tracker();
    advance(850);
    q.playing();
    q.beacon("start");
    q.beacon("heartbeat");
    expect(sent[0]).toMatchObject({ v: 1, seq: 1, kind: "start", startupMs: 850, rebufferCount: 0, rebufferMs: 0 });
    expect(sent[1]).toMatchObject({ seq: 2, startupMs: 850 });
  });

  it("counts stalls after startup, cumulatively, and ignores the initial load and seeks", () => {
    const { q, sent, advance } = tracker();
    q.waiting(); // before the first frame: that is startup, not a rebuffer
    advance(500);
    q.playing();
    q.seekStarted();
    q.waiting(); // caused by the viewer seeking
    q.seekEnded();
    q.playing();
    q.waiting();
    q.waiting(); // the same stall reported twice
    advance(1200);
    q.playing();
    q.beacon("heartbeat");
    q.waiting();
    advance(300);
    q.beacon("end"); // a stall still open is counted up to now
    expect(sent.map((b) => [b.rebufferCount, b.rebufferMs])).toEqual([
      [1, 1200],
      [2, 1500],
    ]);
  });

  it("sends errors once, caps them, and nothing at all after end", () => {
    const { q, sent } = tracker();
    for (let i = 0; i < 12; i++) q.error("network");
    q.beacon("heartbeat");
    q.beacon("heartbeat");
    expect(sent[0].errors).toHaveLength(8);
    expect(sent[1].errors).toBeUndefined();
    expect(q.beacon("end")).toBeDefined();
    expect(q.beacon("heartbeat")).toBeUndefined();
    expect(sent).toHaveLength(3);
  });

  it("carries variant and latency only when known, in whole milliseconds", () => {
    const { q, sent } = tracker();
    q.setVariant("720p");
    q.setLatency(8.4567);
    q.beacon("heartbeat");
    q.setVariant(undefined);
    q.setLatency(Number.NaN);
    q.beacon("heartbeat");
    expect(sent[0]).toMatchObject({ variant: "720p", latencyMs: 8457 });
    expect(sent[1].variant).toBeUndefined();
    expect(sent[1].latencyMs).toBeUndefined();
  });

  it("never puts a field outside the schema into a beacon", () => {
    const { q, sent, advance } = tracker();
    advance(10);
    q.playing();
    q.setVariant("hd");
    q.setLatency(3);
    q.error("media");
    q.beacon("start");
    const allowed = ["v", "sid", "seq", "kind", "startupMs", "rebufferCount", "rebufferMs", "variant", "latencyMs", "errors"];
    expect(Object.keys(sent[0]).filter((k) => !allowed.includes(k))).toEqual([]);
  });
});

describe("error classification", () => {
  it("reduces an hls.js error to a class without reading its URL", () => {
    const withUrl = { type: "networkError", details: "fragLoadError", fatal: true, url: "/playout/a/seg.ts?t=SECRET" };
    expect(classifyHlsError(withUrl)).toBe("network");
    expect(classifyHlsError({ type: "networkError", details: "manifestLoadError" })).toBe("manifest");
    expect(classifyHlsError({ type: "networkError", details: "levelLoadTimeOut" })).toBe("manifest");
    expect(classifyHlsError({ type: "mediaError", details: "manifestIncompatibleCodecsError" })).toBe("codec_unsupported");
    expect(classifyHlsError({ type: "mediaError", details: "bufferAppendError" })).toBe("decode");
    expect(classifyHlsError({ type: "mediaError", details: "bufferStalledError" })).toBe("media");
    expect(classifyHlsError({ type: "otherError", details: "internalException" })).toBe("other");
    expect(classifyHlsError({})).toBe("other");
  });

  it("maps a MediaError code", () => {
    expect([1, 2, 3, 4, undefined].map(classifyMediaError)).toEqual(["media", "network", "decode", "codec_unsupported", "media"]);
  });
});

describe("newSessionId", () => {
  it("is 32 hex characters, which the server's sid pattern accepts", () => {
    const id = newSessionId((n) => new Uint8Array(n).fill(0xab));
    expect(id).toBe("ab".repeat(16));
    expect(/^[A-Za-z0-9_-]{16,64}$/.test(newSessionId())).toBe(true);
  });
});

describe("startup bound (#5)", () => {
  it("leaves startupMs off when it is past the server's bound, and still reports the session as started", () => {
    const { q, sent, advance } = tracker();
    advance(MAX_STARTUP_MS + 1);
    q.playing();
    expect(q.started).toBe(true);
    q.beacon("start");
    expect(sent[0].startupMs).toBeUndefined();
    // And the stall accounting carries on as normal after it.
    q.waiting();
    advance(200);
    q.playing();
    q.beacon("heartbeat");
    expect(sent[1]).toMatchObject({ rebufferCount: 1, rebufferMs: 200 });
  });

  it("measures from the latest reset, so a retry loop before the broadcast is not startup", () => {
    const { q, sent, advance } = tracker();
    advance(60_000); // the page waited a minute for the broadcast
    q.reset(); // the player was recreated when it could begin
    advance(700);
    q.playing();
    q.beacon("start");
    expect(sent[0].startupMs).toBe(700);
  });
});
