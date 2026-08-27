// @vitest-environment jsdom

/* THE PROGRAMME-SCOPED ROUTES THIS CLIENT REACHES, AND THAT IT NAMES ONE.
 *
 * lib/autoApi.ts is a second HTTP client, and it had no notion of a programme
 * at all. Three of the routes it reaches are wrapped in requireSource on the
 * server -- POST /clips, PUT /clips/buffer and PUT /loudness -- so on any
 * install with two programmes all three were refused with 400 source_required
 * and the operator was shown the API's own sentence: `add "?source=<id>"`.
 * Capturing a clip, arming the clip buffer and switching the loudness monitor
 * were simply broken, and nothing failed on a developer's single-source box.
 *
 * These assert the URL, because the URL is the whole defect. A test that
 * stubbed the client and checked its arguments would have passed throughout.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { autoApi } from "./autoApi";

let seen: string[] = [];

beforeEach(() => {
  seen = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      seen.push(String(url));
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

describe("autoApi names the programme on every scoped route", () => {
  it("POST /clips carries the source", async () => {
    await autoApi.captureClip(30, 7);
    expect(seen[0]).toContain("/clips?source=7");
  });

  it("PUT /clips/buffer carries the source", async () => {
    await autoApi.setClipBuffer({ enabled: true, windowSeconds: 60 }, 7);
    expect(seen[0]).toContain("/clips/buffer?source=7");
  });

  it("PUT /loudness carries the source", async () => {
    await autoApi.setLoudnessMonitor(true, 7);
    expect(seen[0]).toContain("/loudness?source=7");
  });

  /* THE READS, which is the half the first fix missed.
   *
   * handleListClips and handleLoudness scope INSIDE the handler rather than
   * carrying requireSource at the router, so surveying the router's middleware
   * -- which is how the first pass looked -- does not find them. Both refuse a
   * two-programme install, so the clip list came back empty and the loudness
   * analyser never reported at all: "NOT UPDATING", "Waiting for the
   * analyser's state", forever, and no per-destination figures for the front
   * page to quote. */
  it("GET /clips carries the source", async () => {
    await autoApi.listClips(7);
    expect(seen[0]).toContain("/clips?source=7");
  });

  it("GET /loudness carries the source", async () => {
    await autoApi.loudness(7);
    expect(seen[0]).toContain("/loudness?source=7");
  });

  it("omits the query entirely when there is no programme", async () => {
    // Null is the zero-source install, which the routes accept: with no
    // sources there is no ambiguity to refuse. Sending `?source=null` would
    // turn that accepted case into a 400.
    await autoApi.captureClip(30, null);
    expect(seen[0]).toContain("/clips");
    expect(seen[0]).not.toContain("source=");
  });
});
