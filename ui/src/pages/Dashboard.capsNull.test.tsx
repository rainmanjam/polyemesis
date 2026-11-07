// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* A TARGET WHOSE caps.fields IS null.
 *
 * X writes no metadata fields. The server used to serialise that nil slice as
 * `"fields": null`, and the Dashboard called `t.caps.fields.includes(...)` on it:
 * "Cannot read properties of null (reading 'includes')" took the whole page
 * down as soon as an X account was connected. The server now always sends an
 * array, but the console must not depend on that. */

import { GoLiveComposer } from "./Dashboard";

const originalFetch = globalThis.fetch;

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url).replace(/^\/api\/v1/, "");
    const body =
      path === "/metadata"
        ? {
            targets: [
              { accountId: 7, platform: "x", accountName: "xacct", caps: { fields: null, scope: "broadcast.write" } },
            ],
          }
        : { accounts: [] };
    return {
      ok: true,
      status: 200,
      headers: new Headers({ "Content-Type": "application/json" }),
      text: async () => JSON.stringify(body),
    } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  globalThis.fetch = originalFetch;
});

describe("GoLiveComposer with a target whose caps.fields is null", () => {
  it("renders instead of throwing from the field filters", async () => {
    render(<GoLiveComposer />);
    expect(await screen.findByLabelText("Title")).toBeTruthy();
  });
});
