// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "./api";
import {
  getDestinationSourceId,
  getRollover,
  getTemplate,
  listOccurrences,
  MAX_THUMBNAIL_BYTES,
  runRollover,
  saveRollover,
  uploadThumbnail,
} from "./recurringApi";

/* The client for #18 and #19. What matters is the wire: a template's absence is a 404 read as "none yet" and not
 * as a failure, the thumbnail goes up as its real image type with the CSRF header, and every rollover call names
 * the destination's programme so a multi-programme install does not refuse it. */

interface Call {
  url: string;
  init: RequestInit;
}

function stubFetch(status: number, body: unknown): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init: RequestInit = {}) => {
      calls.push({ url, init });
      const text = typeof body === "string" ? body : JSON.stringify(body);
      return Promise.resolve(new Response(status === 204 ? null : text, { status }));
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.cookie = "polyemesis_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT";
});

describe("getTemplate", () => {
  it("reads a schedule with no template as null rather than an error", async () => {
    stubFetch(404, { error: "no template" });
    await expect(getTemplate(4)).resolves.toBeNull();
  });

  it("throws every other failure with the server's sentence", async () => {
    stubFetch(500, { error: "database is locked" });
    await expect(getTemplate(4)).rejects.toMatchObject({ status: 500, message: "database is locked" });
  });
});

describe("uploadThumbnail", () => {
  it("sends the raw picture with its real type and the CSRF token from the cookie", async () => {
    document.cookie = "polyemesis_csrf=tok%2B1";
    const calls = stubFetch(204, "");
    const pic = new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: "image/png" });

    await uploadThumbnail(9, pic, "image/png");

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/schedules/9/template/thumbnail");
    expect(calls[0].init.method).toBe("PUT");
    expect(calls[0].init.body).toBe(pic);
    const headers = calls[0].init.headers as Record<string, string>;
    expect(headers["Content-Type"]).toBe("image/png");
    expect(headers["X-CSRF-Token"]).toBe("tok+1");
  });

  it("refuses a wrong type or an oversized picture before any request", async () => {
    const calls = stubFetch(204, "");
    await expect(uploadThumbnail(9, new Blob(["x"]), "image/gif")).rejects.toThrow(/PNG or a JPEG/);
    const big = { size: MAX_THUMBNAIL_BYTES + 1 } as Blob;
    await expect(uploadThumbnail(9, big, "image/jpeg")).rejects.toThrow(/larger than 2 MB/);
    expect(calls).toHaveLength(0);
  });

  it("carries the server's refusal as an ApiError, whether it answered JSON or plain text", async () => {
    stubFetch(415, { error: "the bytes are not a PNG" });
    const err = await uploadThumbnail(9, new Blob(["x"]), "image/png").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 415, message: "the bytes are not a PNG" });

    stubFetch(413, "too big");
    await expect(uploadThumbnail(9, new Blob(["x"]), "image/png")).rejects.toMatchObject({
      status: 413,
      message: "too big",
    });

    stubFetch(500, "");
    await expect(uploadThumbnail(9, new Blob(["x"]), "image/png")).rejects.toMatchObject({
      message: "request failed (500)",
    });
  });
});

describe("listOccurrences", () => {
  it("asks for the window as RFC 3339 instants and treats a non-list answer as empty", async () => {
    const calls = stubFetch(200, { not: "a list" });
    const from = new Date("2026-11-01T00:00:00Z");
    const to = new Date("2026-11-15T00:00:00Z");
    await expect(listOccurrences(from, to)).resolves.toEqual([]);
    expect(calls[0].url).toBe(
      "/api/v1/recurring/occurrences?from=2026-11-01T00%3A00%3A00.000Z&to=2026-11-15T00%3A00%3A00.000Z",
    );
  });
});

describe("rollover calls", () => {
  it("name the destination's programme, and leave it off when the destination has none", async () => {
    const calls = stubFetch(200, { status: "rolled over" });
    await getRollover(7, 3);
    await saveRollover(7, 3, { enabled: true, marginMinutes: 30, chatNotice: false });
    await runRollover(7, null);
    expect(calls.map((c) => `${c.init.method ?? "GET"} ${c.url}`)).toEqual([
      "GET /api/v1/destinations/7/rollover?source=3",
      "PUT /api/v1/destinations/7/rollover?source=3",
      "POST /api/v1/destinations/7/rollover/run",
    ]);
  });

  it("reads the programme from the destination, and null when it carries none", async () => {
    stubFetch(200, { sourceId: 5 });
    await expect(getDestinationSourceId(7)).resolves.toBe(5);
    stubFetch(200, {});
    await expect(getDestinationSourceId(7)).resolves.toBeNull();
  });
});
