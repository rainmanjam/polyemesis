import { describe, expect, it } from "vitest";
import { ApiError, PolyemesisClient, type Transport, type TransportRequest, type TransportResponse } from "../src/api.js";
import { MAX_GAIN } from "../src/trackLevel.js";

// The client side of live track gain (I4): the destination list it reads the
// stored mute and gain from, the track names, and the one write route.

type Reply = TransportResponse | Error;

function fakeTransport(replies: Reply[] = []) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      const next = replies.shift() ?? { status: 200, body: "{}" };
      if (next instanceof Error) throw next;
      return next;
    },
  };
  return { transport, sent };
}

const BASE = new URL("https://studio.example");
const ok = (body: unknown): Reply => ({ status: 200, body: JSON.stringify(body) });
const client = (t: Transport) => new PolyemesisClient(BASE, "t", t);

/** One row of GET /destinations as the server sends it: wrapped, with its compiled routing beside it. */
function row(over: Record<string, unknown> = {}, profile: Record<string, unknown> = {}) {
  return {
    destination: {
      id: 5,
      name: "Twitch",
      kind: "rtmp",
      platform: "twitch",
      sourceId: 7,
      streamKey: "live_secret",
      audio: {},
      profile: {
        mode: "simple",
        tracks: [
          { track: 0, enabled: true, gain: 1 },
          { track: 2, enabled: true, gain: 0.5, muted: true },
        ],
        ...profile,
      },
      ...over,
    },
    routing: { filter: "..." },
  };
}

describe("destinations()", () => {
  it("reads every destination's stored track levels, and keeps nothing it does not need", async () => {
    const { transport, sent } = fakeTransport([ok([row()])]);
    const [d] = await client(transport).destinations();
    expect(sent[0]!.method).toBe("GET");
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations");
    expect(d).toEqual({
      id: 5,
      name: "Twitch",
      sourceId: 7,
      matrix: false,
      copyAudio: false,
      tracks: [
        { track: 0, enabled: true, gain: 1, muted: false },
        { track: 2, enabled: true, gain: 0.5, muted: true },
      ],
    });
    // The stream key is a publish credential. It is not carried into the plugin's cache.
    expect(JSON.stringify(d)).not.toContain("live_secret");
  });

  it("marks a matrix mix, where the server takes no track gain", async () => {
    const { transport } = fakeTransport([ok([row({}, { mode: "matrix" })])]);
    expect((await client(transport).destinations())[0]!.matrix).toBe(true);
  });

  it("marks audio copy, which has no level stage, but not on an audio-only destination", async () => {
    const { transport } = fakeTransport([
      ok([row({ audio: { copy: true } }), row({ id: 6, kind: "audio", audio: { copy: true } })]),
    ]);
    const [video, radio] = await client(transport).destinations();
    expect(video!.copyAudio).toBe(true);
    expect(radio!.copyAudio).toBe(false);
  });

  it("leaves sourceId out for a legacy row that has none", async () => {
    const { transport } = fakeTransport([ok([row({ sourceId: undefined })])]);
    expect((await client(transport).destinations())[0]!.sourceId).toBeUndefined();
  });

  it("refuses a list that is not the server's shape", async () => {
    for (const body of [{ destinations: [] }, [{ destination: { id: "5" } }], [{ destination: { id: 5, name: "x", profile: { tracks: [{ track: 0 }] } } }]]) {
      const { transport } = fakeTransport([ok(body)]);
      await expect(client(transport).destinations()).rejects.toMatchObject({ kind: "malformed" });
    }
  });
});

describe("trackNames()", () => {
  it("prefers the operator's label, then the container title, per 0-based track", async () => {
    const { transport, sent } = fakeTransport([
      ok({
        tracks: [
          { index: 0, title: "Main" },
          { index: 1, title: "" },
          { index: 2, title: "Stream 3" },
        ],
        annotations: [{ track: 2, label: "Guest mic" }, { track: 1, role: "music" }],
      }),
    ]);
    const names = await client(transport).trackNames(7);
    expect(sent[0]!.url.pathname).toBe("/api/v1/source");
    expect(sent[0]!.url.searchParams.get("source")).toBe("7");
    expect([...names.entries()]).toEqual([
      [0, "Main"],
      [2, "Guest mic"],
    ]);
  });

  it("answers no names, not an error, for a source with no layout yet", async () => {
    const { transport } = fakeTransport([ok({ probed: false })]);
    expect((await client(transport).trackNames()).size).toBe(0);
  });
});

describe("setTrackLevel()", () => {
  it("posts the mute to the track's gain route, scoped to the programme", async () => {
    const { transport, sent } = fakeTransport([ok({ applied: "live", destination: row().destination })]);
    const out = await client(transport).setTrackLevel(5, 2, { muted: false }, 7);
    expect(sent[0]!.method).toBe("POST");
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations/5/tracks/2/gain");
    expect(sent[0]!.url.searchParams.get("source")).toBe("7");
    expect(JSON.parse(sent[0]!.body!)).toEqual({ muted: false });
    expect(out.applied).toBe("live");
    expect(out.destination?.id).toBe(5);
  });

  it("carries the restart reason through unchanged", async () => {
    const { transport } = fakeTransport([
      ok({ applied: "restart", reason: "the platform has no verified live path", destination: row().destination }),
    ]);
    const out = await client(transport).setTrackLevel(5, 0, { gain: 0.5 }, 7);
    expect(out).toMatchObject({ applied: "restart", reason: "the platform has no verified live path" });
  });

  it("refuses an answer that does not say how the change applied", async () => {
    const { transport } = fakeTransport([ok({ applied: "maybe" })]);
    await expect(client(transport).setTrackLevel(5, 0, { muted: true })).rejects.toMatchObject({ kind: "malformed" });
  });

  it("keeps the server's own words on a 409 and a 400", async () => {
    const { transport } = fakeTransport([
      { status: 409, body: JSON.stringify({ error: "this destination passes its audio through unchanged" }) },
      { status: 400, body: JSON.stringify({ error: "a matrix mix takes its gain per cell" }) },
    ]);
    const c = client(transport);
    await expect(c.setTrackLevel(5, 0, { muted: true })).rejects.toMatchObject({
      kind: "http",
      status: 409,
      message: "this destination passes its audio through unchanged",
    });
    await expect(c.setTrackLevel(5, 0, { gain: 0.5 })).rejects.toMatchObject({ kind: "http", status: 400 });
  });

  it("never sends a gain the server would refuse or that exceeds its cap", async () => {
    const { transport, sent } = fakeTransport();
    const c = client(transport);
    for (const gain of [0, -1, Number.NaN, Number.POSITIVE_INFINITY, MAX_GAIN + 0.001]) {
      await expect(c.setTrackLevel(5, 0, { gain })).rejects.toBeInstanceOf(RangeError);
    }
    await expect(c.setTrackLevel(5, -1, { muted: true })).rejects.toBeInstanceOf(RangeError);
    await expect(c.setTrackLevel(5, 0, {})).rejects.toBeInstanceOf(RangeError);
    expect(sent).toHaveLength(0);
  });

  it("is an ApiError for no answer at all, like every other route", async () => {
    const { transport } = fakeTransport([new Error("ECONNREFUSED")]);
    await expect(client(transport).setTrackLevel(5, 0, { muted: true })).rejects.toBeInstanceOf(ApiError);
  });
});
