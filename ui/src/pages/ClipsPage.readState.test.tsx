// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* AN EMPTY STATE IS A POSITIVE CLAIM.
 *
 * "No clips yet." says the server answered and there is nothing there. This
 * page stored a failed GET /clips as `view = null` and dropped `loading` to
 * false in the same `.finally`, so a 500 drew that sentence over a machine
 * that may well have had clips on it -- and an operator who had just captured
 * one would go looking for where it went.
 *
 * It is the exact bug lib/readState.ts was written to forbid, on a page that
 * did not use it.
 *
 * The stub is a plain function rather than a `vi.fn()`: vitest records a
 * mock's settled results by chaining onto the returned promise, and that
 * derived promise rejects with nobody watching, which the runner then reports
 * as this test's own failure. Nothing to do with the page. */

let clipsResponse: () => Promise<unknown> = () => Promise.resolve(null);

vi.mock("@/lib/autoApi", () => ({
  autoApi: {
    listClips: () => clipsResponse(),
    captureClip: () => Promise.resolve({}),
    setClipBuffer: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ programme: "main", programmeKnown: true }),
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { ClipsPage } = await import("./ClipsPage");
const { translate } = await import("@/lib/i18n");

afterEach(cleanup);

const answered = {
  clips: [],
  buffer: { enabled: false, running: false, buffer: null },
  usage: { count: 0, usedBytes: 0, maxBytes: 0, maxClips: 0 },
  bounds: { minWindowSeconds: 5, maxWindowSeconds: 300 },
};

describe("ClipsPage and a read that failed", () => {
  it("does not claim the clip list is empty when it could not be read", async () => {
    clipsResponse = () => Promise.reject(new Error("boom"));
    render(<ClipsPage />);

    await waitFor(() =>
      expect(screen.getByText(translate("en", "clips.listUnread"))).toBeTruthy(),
    );
    expect(
      screen.queryByText(translate("en", "clips.noneYet")),
      "a failed read rendered the empty state, which asserts the server answered",
    ).toBeNull();
  });

  it("still says so when the server really did answer with nothing", async () => {
    clipsResponse = () => Promise.resolve(answered);
    render(<ClipsPage />);

    await waitFor(() =>
      expect(screen.getByText(translate("en", "clips.noneYet"))).toBeTruthy(),
    );
    expect(screen.queryByText(translate("en", "clips.listUnread"))).toBeNull();
  });
});
