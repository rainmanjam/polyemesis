// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "./api";
import type { DestinationId } from "./types";

/* The YouTube picker calls: the path, the verb, the optional destination id, the encoded event id, and a refusal
 * surfacing as an ApiError with the server's message. */

function fakeFetch(status = 200, body: unknown = {}) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
  });
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

const path = (u: string) => u.replace(/^.*\/api\/v1/, "");

describe("the YouTube picker api calls", () => {
  it("asks for streams and events with the destination only when given one", async () => {
    const calls = fakeFetch(200, { supported: true, streams: [], events: [] });
    await api.youtubeStreams(3);
    await api.youtubeStreams(3, 7 as DestinationId);
    await api.youtubeEvents(3);
    await api.youtubeEvents(3, 7 as DestinationId);
    expect(calls.map((c) => `${c.init?.method ?? "GET"} ${path(c.url)}`)).toEqual([
      "GET /platforms/accounts/3/youtube/streams",
      "GET /platforms/accounts/3/youtube/streams?destinationId=7",
      "GET /platforms/accounts/3/youtube/events",
      "GET /platforms/accounts/3/youtube/events?destinationId=7",
    ]);
  });

  it("turns auto-start on with a POST to the event, encoding its id", async () => {
    const calls = fakeFetch(200, { eventId: "a/b", enableAutoStart: true });
    const r = await api.youtubeEventAutoStart(3, "a/b");
    expect(r.enableAutoStart).toBe(true);
    expect(`${calls[0].init?.method} ${path(calls[0].url)}`).toBe("POST /platforms/accounts/3/youtube/events/a%2Fb/auto-start");
  });

  it("surfaces a refusal as an ApiError carrying the server's sentence", async () => {
    fakeFetch(502, { error: "this event is live, and YouTube accepts no change to auto-start" });
    await expect(api.youtubeEventAutoStart(3, "ev-1")).rejects.toMatchObject({
      status: 502,
      message: expect.stringContaining("accepts no change"),
    });
    fakeFetch(404, { error: "not found" });
    const err = await api.youtubeStreams(99).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(404);
  });
});

describe("the Studio events call", () => {
  it("reads Studio's events from one route", async () => {
    const calls = fakeFetch(200, { events: [], stale: ["chan"] });
    const r = await api.youtubeStudioEvents();
    expect(r.stale).toEqual(["chan"]);
    expect(path(calls[0].url)).toBe("/youtube/studio-events");
  });
});
