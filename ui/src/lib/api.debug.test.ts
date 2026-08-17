// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, api } from "./api";

/* THE ONE CALL IN THIS MODULE THAT DOES NOT GO THROUGH request().
 *
 * Every other endpoint is a one-line get/put and its behaviour is the shared
 * helper's, already exercised everywhere. exportDebug is not: it builds its own
 * fetch because request() parses the body as JSON and this response is a raw
 * bundle whose NAME lives in a header. That hand-rolled path is where the
 * mistakes fit, and all three of them are silent:
 *
 *   a missing CSRF header — rejected by the server, and the operator sees a
 *   generic failure on the one control that has an audit consequence
 *
 *   a filename parse that misses — the bundle still downloads, so nothing looks
 *   wrong, and two captures from one support thread arrive named identically
 *
 *   an error path that reports the status instead of the server's sentence —
 *   "export failed (412)" where the server said what to do about it
 */

const realFetch = globalThis.fetch;

function respond(
  body: string,
  init: { status?: number; disposition?: string } = {},
): Response {
  const headers = new Headers();
  if (init.disposition) headers.set("Content-Disposition", init.disposition);
  return {
    ok: (init.status ?? 200) < 400,
    status: init.status ?? 200,
    headers,
    text: async () => body,
  } as unknown as Response;
}

beforeEach(() => {
  document.cookie = "polyemesis_csrf=tok-abc123";
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("api.exportDebug", () => {
  it("POSTs with the CSRF token, because a GET would be prefetchable", async () => {
    const f = vi.fn(async () => respond("{}", { disposition: 'attachment; filename="x.json"' }));
    globalThis.fetch = f as unknown as typeof fetch;

    await api.exportDebug();

    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/debug/export");
    // A POST so the export is CSRF-covered and so a browser following a link
    // cannot put an entry in the audit trail.
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok-abc123");
  });

  it("takes the filename the SERVER chose", async () => {
    globalThis.fetch = (async () =>
      respond('{"records":[]}', {
        disposition: 'attachment; filename="polyemesis-debug-20260817-120000.json"',
      })) as unknown as typeof fetch;

    const { text, filename } = await api.exportDebug();

    // The timestamp is the whole point: two bundles in one support thread are
    // otherwise indistinguishable to whoever receives them.
    expect(filename).toBe("polyemesis-debug-20260817-120000.json");
    expect(text).toBe('{"records":[]}');
  });

  it("falls back to a generic name rather than saving something unnamed", async () => {
    globalThis.fetch = (async () => respond("{}")) as unknown as typeof fetch;

    expect((await api.exportDebug()).filename).toBe("polyemesis-debug.json");
  });

  it("reports the SERVER'S sentence, not the status code", async () => {
    globalThis.fetch = (async () =>
      respond('{"error":"debug recording is not available on this build"}', {
        status: 412,
      })) as unknown as typeof fetch;

    // "export failed (412)" tells an operator nothing they can act on; the
    // server's own line says what is actually wrong.
    await expect(api.exportDebug()).rejects.toThrow(/not available on this build/);
    await expect(api.exportDebug()).rejects.toBeInstanceOf(ApiError);
  });

  it("still fails legibly when the body is not JSON at all", async () => {
    // A proxy returning HTML is the realistic shape of this: JSON.parse throws,
    // and swallowing that would surface an unhandled rejection instead of an
    // error the UI can render.
    globalThis.fetch = (async () =>
      respond("<html>502 Bad Gateway</html>", { status: 502 })) as unknown as typeof fetch;

    await expect(api.exportDebug()).rejects.toThrow(/export failed \(502\)/);
  });
});
