// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { OverlaySettings } from "./OverlaySettings";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { ChatOverlay, OverlayPreset, OverlayStyle, OverlaysResponse } from "@/lib/overlay";

/* The overlay counters since the server started. Three counts, so "label:
 * value" pairs rather than a One key per count: the old sentence read
 * "1 lines shown, 1 held back by moderation, 1 dropped waiting for automod". */

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  setLanguage("en");
});

const classic: OverlayStyle = {
  preset: "classic", font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 30, maxLines: 12, showPlatform: true, showBadges: true,
};
const presets: Record<OverlayPreset, OverlayStyle> = {
  classic,
  bubble: { ...classic, preset: "bubble", font: "rounded", fontSize: 22, backgroundColor: "#111827", backgroundOpacity: 75, maxLines: 8 },
  outline: { ...classic, preset: "outline", fontSize: 28, maxLines: 6, showPlatform: false },
  compact: { ...classic, preset: "compact", font: "mono", fontSize: 16, maxLines: 20 },
};
const overlay: ChatOverlay = {
  id: 7, name: "All", obsKey: ["pov", "testkey123456789"].join("_"), platforms: [], style: classic,
  createdAt: "", keyRotatedAt: "",
};

/** One overlay, so the list (and the counters above it) is drawn at all. */
function overlays(n: number): OverlaysResponse {
  return {
    overlays: [overlay],
    presets,
    presetOrder: ["classic", "bubble", "outline", "compact"],
    platforms: ["twitch", "youtube", "kick"],
    stats: { shown: n, blocked: n, expired: n },
    maxHoldMs: 4000,
  };
}

describe("OverlaySettings: overlay.stats (en)", () => {
  it("reads shown: 1 · held back by moderation: 1 · dropped waiting for automod: 1, not 1 lines shown", async () => {
    vi.spyOn(api, "listOverlays").mockResolvedValue(overlays(1));
    render(<OverlaySettings />);
    expect(
      await screen.findByText(
        "Since the server started — shown: 1 · held back by moderation: 1 · dropped waiting for automod: 1.",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByText(
        "Since the server started: 1 lines shown, 1 held back by moderation, 1 dropped waiting for automod.",
      ),
    ).toBeNull();
  });

  it("reads shown: 2 · held back by moderation: 2 · dropped waiting for automod: 2", async () => {
    vi.spyOn(api, "listOverlays").mockResolvedValue(overlays(2));
    render(<OverlaySettings />);
    expect(
      await screen.findByText(
        "Since the server started — shown: 2 · held back by moderation: 2 · dropped waiting for automod: 2.",
      ),
    ).toBeTruthy();
  });
});
