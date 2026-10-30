// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { OverlayEditor } from "./OverlaySettings";
import { ChatOverlayView } from "./overlay/ChatOverlayView";
import type { ChatOverlay, OverlayPreset, OverlayStyle } from "@/lib/overlay";

const updateOverlay = vi.fn(async () => ({}));
const rotateOverlayKey = vi.fn(async () => ({}));
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      updateOverlay: (...a: unknown[]) => updateOverlay(...(a as [])),
      rotateOverlayKey: (...a: unknown[]) => rotateOverlayKey(...(a as [])),
      revokeOverlayKey: vi.fn(),
      deleteOverlay: vi.fn(),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

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
const meta = {
  presets,
  presetOrder: ["classic", "bubble", "outline", "compact"] as OverlayPreset[],
  platforms: ["twitch", "youtube", "kick", "facebook", "rumble"],
  maxHoldMs: 4000,
};
const overlay = (o: Partial<ChatOverlay> = {}): ChatOverlay => ({
  id: 7, name: "All", obsKey: "pov_testkey123456789", platforms: [], style: classic,
  createdAt: "", keyRotatedAt: "", ...o,
});

const writeText = vi.fn(async () => {});
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText } });
  updateOverlay.mockClear();
  writeText.mockClear();
});
afterEach(cleanup);

const previewPlatforms = () =>
  Array.from(screen.getByTestId("overlay-preview").querySelectorAll("[data-platform]")).map(
    (e) => e.getAttribute("data-platform"),
  );

describe("the overlay style editor", () => {
  it("previews every platform until a filter is chosen, then only that platform", () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    expect(new Set(previewPlatforms())).toEqual(new Set(["twitch", "youtube", "kick"]));
    fireEvent.click(screen.getByRole("switch", { name: "Twitch" }));
    expect(new Set(previewPlatforms())).toEqual(new Set(["twitch"]));
  });

  it("applies a knob to the live preview at once, before saving", () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Font size (px)"), { target: { value: "40" } });
    const log = within(screen.getByTestId("overlay-preview")).getByRole("log");
    expect(log.style.fontSize).toBe("40px");
    expect(screen.getByText(/Unsaved changes/)).toBeTruthy();
    expect(updateOverlay).not.toHaveBeenCalled();
  });

  it("clamps a number to the server's bounds", () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Maximum lines"), { target: { value: "500" } });
    expect((screen.getByLabelText("Maximum lines") as HTMLInputElement).value).toBe("50");
    fireEvent.change(screen.getByLabelText("Font size (px)"), { target: { value: "1" } });
    expect((screen.getByLabelText("Font size (px)") as HTMLInputElement).value).toBe("10");
  });

  it("limits the preview to maxLines", () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Maximum lines"), { target: { value: "2" } });
    expect(previewPlatforms()).toHaveLength(2);
  });

  it("saves exactly the edited style and platforms", async () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.click(screen.getByRole("switch", { name: "Twitch" }));
    fireEvent.change(screen.getByLabelText("Fade after (seconds)"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: /Save overlay/ }));
    await vi.waitFor(() => expect(updateOverlay).toHaveBeenCalled());
    expect(updateOverlay).toHaveBeenCalledWith(7, {
      name: "All", platforms: ["twitch"], style: { ...classic, fadeSeconds: 0 },
    });
  });

  it("copies an OBS URL with the key in the fragment", async () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: /Copy OBS URL$/ }));
    fireEvent.click(screen.getByRole("button", { name: /Copy dock URL$/ }));
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(writeText).toHaveBeenNthCalledWith(1, `${window.location.origin}/overlay#key=pov_testkey123456789`);
    expect(writeText).toHaveBeenNthCalledWith(2, `${window.location.origin}/overlay/dock#key=pov_testkey123456789`);
  });

  // The path form is offered beside the fragment form, not instead of it: the
  // fragment never reaches any server, and a path reaches a reverse proxy's log.
  it("also offers the key-in-path URLs, for an OBS that drops the fragment, with the trade-off said", async () => {
    render(<OverlayEditor overlay={overlay()} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: /Copy OBS URL \(key in path\)/ }));
    fireEvent.click(screen.getByRole("button", { name: /Copy dock URL \(key in path\)/ }));
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(writeText).toHaveBeenNthCalledWith(1, `${window.location.origin}/overlay/k/pov_testkey123456789`);
    expect(writeText).toHaveBeenNthCalledWith(2, `${window.location.origin}/overlay/dock/k/pov_testkey123456789`);
    expect(screen.getByText(/reverse proxy/)).toBeTruthy();
  });

  it("offers no URL for a revoked overlay, only a new key", () => {
    render(<OverlayEditor overlay={overlay({ obsKey: "" })} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /Copy OBS URL/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Issue a new key/ }));
    expect(rotateOverlayKey).toHaveBeenCalledWith(7);
  });
});

describe("the overlay view", () => {
  const lines = [{ id: "a", platform: "twitch", name: "viewer", text: "hello", at: "", receivedAt: 0, color: "#ff0000" }];

  it("is display-only: no buttons, links or inputs", () => {
    for (const mode of ["overlay", "dock", "preview"] as const) {
      const { container } = render(<ChatOverlayView lines={lines} style={classic} mode={mode} />);
      expect(container.querySelectorAll("button, a, input, select, textarea")).toHaveLength(0);
      cleanup();
    }
  });

  it("draws the overlay over a transparent background and the dock over a solid one", () => {
    render(<ChatOverlayView lines={lines} style={classic} mode="overlay" />);
    expect(screen.getByRole("log").style.background).toBe("transparent");
    cleanup();
    render(<ChatOverlayView lines={lines} style={classic} mode="dock" />);
    expect(screen.getByRole("log").style.background).not.toBe("transparent");
  });

  it("uses the viewer's colour only when asked to", () => {
    render(<ChatOverlayView lines={lines} style={{ ...classic, authorColors: false }} mode="overlay" />);
    expect((screen.getByText("viewer") as HTMLElement).style.color).toBe("rgb(255, 255, 255)");
  });
});
