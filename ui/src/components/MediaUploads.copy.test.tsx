// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* "Pull URL" on a media row awaited navigator.clipboard.writeText with no
 * catch. On a plain-http install navigator.clipboard is undefined, so the click
 * threw inside an async handler: an unhandled rejection, no toast, and the
 * operator believed the URL was on the clipboard. */

const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("sonner", () => ({ toast }));
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      media: () =>
        Promise.resolve([
          { name: "loop.mp4", size: 1, modTime: "2026-01-01T00:00:00Z", pullUrl: "srt://h:1/loop" },
        ]),
    },
  };
});

const { MediaUploads } = await import("./MediaUploads");

afterEach(() => {
  cleanup();
  toast.error.mockReset();
  Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
});

describe("MediaUploads copy", () => {
  it("says so when the clipboard is unavailable", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    render(<MediaUploads />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy the pull URL for loop.mp4" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });
});
