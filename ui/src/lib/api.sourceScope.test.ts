// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "./api";

/* WHICH PROGRAMME EVERY REQUEST IS FOR — issue #497.
 *
 * These five calls act on ONE programme and none of them used to say which, so
 * the server resolved its default engine: Engines()[0], the right answer only
 * on an install with a single source. Editing a track label while looking at
 * programme 2 therefore rewrote programme 1's ingest and restarted its live
 * destinations — no click and no confirmation, just the routing editor's 500 ms
 * autosave.
 *
 * The assertion is on the URL rather than on a rendered screen because the URL
 * is the whole of the fix on this side: the argument exists to reach the wire,
 * and a caller that accepts `sourceId` and drops it fails in exactly the way
 * the bug did — silently, with a 200 back.
 *
 * The omitted case is asserted too, and it asserts an ABSENCE: no `source=` at
 * all rather than a guessed one. A destination row written before sources
 * existed carries no id, and inventing one for it here would move the bug into
 * the client. The server refuses those on a multi-source install. */

const realFetch = globalThis.fetch;
let seen: string[] = [];

function stubFetch() {
  return vi.fn(async (url: string) => {
    seen.push(url);
    return {
      ok: true,
      status: 200,
      text: async () => "{}",
    } as unknown as Response;
  });
}

beforeEach(() => {
  seen = [];
  document.cookie = "polyemesis_csrf=tok-abc123";
  globalThis.fetch = stubFetch() as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

const calls: { name: string; scoped: () => Promise<unknown>; unscoped: () => Promise<unknown> }[] = [
  {
    name: "putAnnotations — writes the ingest and restarts its destinations",
    scoped: () => api.putAnnotations([], 7),
    unscoped: () => api.putAnnotations([]),
  },
  {
    name: "compileRouting — the filter string the editor shows",
    scoped: () => api.compileRouting({} as never, 7),
    unscoped: () => api.compileRouting({} as never),
  },
  {
    name: "applyPreset — picks track indices out of an ingest",
    scoped: () => api.applyPreset("mic-only", {} as never, 7),
    unscoped: () => api.applyPreset("mic-only", {} as never),
  },
  {
    name: "listProcesses — every engine names its children identically",
    scoped: () => api.listProcesses(7),
    unscoped: () => api.listProcesses(),
  },
  {
    name: "processLogs — one child's FFmpeg log",
    scoped: () => api.processLogs("ingest", 7),
    unscoped: () => api.processLogs("ingest"),
  },
];

describe("every programme-scoped call carries the source id it was given", () => {
  for (const c of calls) {
    it(c.name, async () => {
      await c.scoped();
      expect(seen).toHaveLength(1);
      expect(seen[0]).toContain("source=7");
    });
  }
});

describe("and invents none when it was given none", () => {
  for (const c of calls) {
    it(c.name, async () => {
      await c.unscoped();
      expect(seen).toHaveLength(1);
      expect(seen[0]).not.toContain("source=");
    });
  }
});

it("null is the same absence as undefined, because that is what a legacy row reads as", async () => {
  await api.putAnnotations([], null);
  expect(seen[0]).not.toContain("source=");
});
