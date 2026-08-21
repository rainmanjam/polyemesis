// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { api } from "./api";
import { asDestinationId, type RoutingProfile } from "./types";

/* #9: handleCompileRouting and handleApplyPreset had nothing to compile
 * against but the shared default engine's source (`s.eng().Source()`,
 * engines[0]) unless the request named a destination -- correct on a
 * single-source install, and wrong on a multi-source one for exactly the
 * reason PR #478 fixed the four routes this bug outlived. The server side
 * (routingSourceOverride, internal/api/handlers.go) now resolves an optional
 * `?destinationId=` query parameter; this pins that the browser actually
 * sends it, which is the half the server-side fix could not do by itself.
 * Modelled on api.device.test.ts's fetch-capture pattern.
 */

const realFetch = globalThis.fetch;

function respond(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers(),
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

let calls: Array<[string, RequestInit]>;

beforeEach(() => {
  document.cookie = "polyemesis_csrf=tok-abc123";
  calls = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

function capture(response: Response): void {
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push([url, init]);
    return response;
  }) as unknown as typeof fetch;
}

const profile: RoutingProfile = {
  mode: "simple",
  tracks: [],
  matrix: null,
  normalize: "off",
  sampleRate: 48000,
};

describe("api.compileRouting", () => {
  it("sends the destination it is compiling for, when told one", async () => {
    capture(respond({ routing: {}, profile }));

    await api.compileRouting(profile, asDestinationId(7));

    expect(calls[0][0]).toBe("/api/v1/routing/compile?destinationId=7");
  });

  it("sends no destinationId when the caller has none to give", async () => {
    // The compat path: routingSourceOverride falls back to the shared
    // default engine's source when the parameter is absent, so an
    // omitted id must arrive as an omitted parameter, not as
    // `destinationId=undefined`.
    capture(respond({ routing: {}, profile }));

    await api.compileRouting(profile);

    expect(calls[0][0]).toBe("/api/v1/routing/compile");
  });
});

describe("api.applyPreset", () => {
  it("sends the destination the preset is being applied to", async () => {
    capture(respond({ profile, routing: {} }));

    await api.applyPreset(
      "obs-default",
      { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1 },
      asDestinationId(12),
    );

    expect(calls[0][0]).toBe("/api/v1/routing/presets/obs-default?destinationId=12");
  });
});
