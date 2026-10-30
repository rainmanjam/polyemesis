import { describe, expect, it } from "vitest";
import {
  ApiError,
  PolyemesisClient,
  type Transport,
  type TransportRequest,
  type TransportResponse,
} from "../src/api.js";

// The routes the new keys call: per-destination stop preview, the Record switch,
// and the routing preset list and apply. The fake transport answers from a queue.

function fakeTransport(replies: TransportResponse[] = []) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      return replies.shift() ?? { status: 200, body: "{}" };
    },
  };
  return { transport, sent };
}

const BASE = new URL("https://studio.example:443");
const ok = (body: unknown): TransportResponse => ({ status: 200, body: JSON.stringify(body) });

describe("stopImpactFor", () => {
  it("asks for one destination by id, with no programme when the key names none", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: 1, youtubeBroadcasts: 1 })]);
    const impact = await new PolyemesisClient(BASE, "t", transport).stopImpactFor(42);
    expect(impact).toEqual({ destinations: 1, youtubeBroadcasts: 1 });
    expect(sent[0]!.method).toBe("GET");
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations/stop-impact");
    expect(sent[0]!.url.searchParams.get("destination")).toBe("42");
    expect(sent[0]!.url.searchParams.has("source")).toBe(false);
  });

  it("names the key's programme, so the server can refuse a destination on another", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: 1, youtubeBroadcasts: 0 })]);
    await new PolyemesisClient(BASE, "t", transport).stopImpactFor(42, 7);
    expect(sent[0]!.url.searchParams.get("destination")).toBe("42");
    expect(sent[0]!.url.searchParams.get("source")).toBe("7");
  });

  it("refuses to answer when the server says the destination is unknown", async () => {
    const { transport } = fakeTransport([{ status: 404, body: '{"error":"not found"}' }]);
    await expect(new PolyemesisClient(BASE, "t", transport).stopImpactFor(9)).rejects.toMatchObject({
      kind: "http",
      status: 404,
    });
  });
});

describe("recording", () => {
  it("reads the install-wide state", async () => {
    const { transport, sent } = fakeTransport([
      ok({ enabled: true, state: "halted_disk", halted: true, reason: "disk full", installWide: true }),
    ]);
    const state = await new PolyemesisClient(BASE, "t", transport).recording();
    expect(state.state).toBe("halted_disk");
    expect(sent[0]!.method).toBe("GET");
    expect(sent[0]!.url.pathname).toBe("/api/v1/recording");
  });

  it("sends the switch as a JSON body and never a source", async () => {
    const { transport, sent } = fakeTransport([
      ok({ enabled: false, state: "off", halted: false, installWide: true }),
    ]);
    await new PolyemesisClient(BASE, "t", transport).setRecording(false);
    expect(sent[0]!.method).toBe("POST");
    expect(sent[0]!.url.pathname).toBe("/api/v1/recording");
    expect(JSON.parse(sent[0]!.body!)).toEqual({ enabled: false });
    expect(sent[0]!.url.searchParams.has("source")).toBe(false);
  });
});

describe("routingPresets", () => {
  it("returns id and name for each preset and ignores the rest", async () => {
    const { transport } = fakeTransport([
      ok({
        presets: [
          { id: "everything", name: "Everything", description: "all tracks", needsMicTrack: false },
          { id: "music", name: "Music", description: "music only" },
        ],
        defaults: {},
      }),
    ]);
    expect(await new PolyemesisClient(BASE, "t", transport).routingPresets()).toEqual([
      { id: "everything", name: "Everything" },
      { id: "music", name: "Music" },
    ]);
  });

  it("refuses a list that has no ids, rather than showing blank keys", async () => {
    const { transport } = fakeTransport([ok({ presets: [{ name: "No id" }] })]);
    await expect(new PolyemesisClient(BASE, "t", transport).routingPresets()).rejects.toMatchObject({
      kind: "malformed",
    });
  });
});

describe("applyPreset", () => {
  it("posts to the preset path, encoded, scoped to the programme, with no body", async () => {
    const { transport, sent } = fakeTransport([ok({ ok: true })]);
    await new PolyemesisClient(BASE, "t", transport).applyPreset("music/live", 3);
    expect(sent[0]!.method).toBe("POST");
    expect(sent[0]!.url.pathname).toBe("/api/v1/routing/presets/music%2Flive");
    expect(sent[0]!.url.searchParams.get("source")).toBe("3");
    expect(sent[0]!.body).toBeUndefined();
  });
});

describe("no programme", () => {
  it("maps a 503 with the no_source code to its own kind, not a generic http error", async () => {
    const body = JSON.stringify({ error: "this install has no source yet", code: "no_source" });
    const { transport } = fakeTransport([{ status: 503, body }]);
    const err: unknown = await new PolyemesisClient(BASE, "t", transport).applyPreset("everything").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).kind).toBe("noSource");
    expect((err as ApiError).status).toBe(503);
    expect((err as ApiError).message).toBe("this install has no source yet");
  });

  it("keeps any other 503 as an ordinary http error", async () => {
    const { transport } = fakeTransport([{ status: 503, body: '{"error":"busy"}' }]);
    const err: any = await new PolyemesisClient(BASE, "t", transport).applyPreset("everything").catch((e) => e);
    expect(err.kind).toBe("http");
  });
});
