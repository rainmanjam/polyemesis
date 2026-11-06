// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { Settings } from "@/lib/types";

/* The ingest URL's Copy button awaited navigator.clipboard.writeText with no
 * catch. On a plain-http install navigator.clipboard is undefined, so the click
 * threw in an async handler: an unhandled rejection and no word to the operator. */

const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("sonner", () => ({ toast }));
vi.mock("@/hooks/useLiveData", () => ({
  useIngestLive: () => false,
  useLiveData: () => ({ source: null, bitrate: [] }),
}));

const settings = {
  display: { timeZone: "" },
  ingest: { mode: "srt", srt: { passphrase: "", latencyMs: 120 }, rtmp: { app: "live", streamKey: "" } },
  listeners: { srtPort: 6000, rtmpPort: 1935 },
  failover: { enabled: false, graceSeconds: 10, return: "manual", slate: { enabled: false, imagePath: "", color: "black" } },
  multitrack: { gpus: [] },
} as unknown as Settings;

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      getSettings: () => Promise.resolve(settings),
      listSources: () => Promise.resolve([{ id: 1, name: "Studio" }]),
      system: () => Promise.resolve({ version: "test", ffmpeg: {}, ingestUrl: "srt://studio.example:6000" }),
      putSettings: (next: Settings) => Promise.resolve(next),
      automodMatrix: () => Promise.resolve({ platforms: [], actions: [], checkers: [], cells: [] }),
      completeTour: () => Promise.resolve(),
    },
  };
});

const { SettingsPage } = await import("./SettingsPage");
const { translate } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  toast.error.mockReset();
  toast.success.mockReset();
  Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
});

async function copyIngest() {
  render(
    <MemoryRouter initialEntries={["/settings?tab=ingest"]}>
      <SettingsPage />
    </MemoryRouter>,
  );
  fireEvent.click((await screen.findAllByRole("button", { name: translate("en", "set.copy") }))[0]);
}

describe("copying the ingest URL on the Ingest tab", () => {
  it("says so when the clipboard is unavailable, instead of failing silently", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    await copyIngest();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("confirms when it copied", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await copyIngest();
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
    expect(writeText).toHaveBeenCalledWith("srt://studio.example:6000");
  });
});
