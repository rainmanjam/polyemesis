// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { OverlayEditor } from "./OverlaySettings";
import { translate } from "@/lib/i18n";
import { DEFAULT_ALERT_STYLE, type ChatOverlay, type OverlayPreset, type OverlayStyle } from "@/lib/overlay";

const updateOverlay = vi.fn(async () => ({}));
const sendTestActivity = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      updateOverlay: (...a: unknown[]) => updateOverlay(...(a as [])),
      rotateOverlayKey: vi.fn(),
      revokeOverlayKey: vi.fn(),
      deleteOverlay: vi.fn(),
      sendTestActivity: (...a: unknown[]) => sendTestActivity(...a),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const T = (k: Parameters<typeof translate>[1]) => translate("en", k);

const classic: OverlayStyle = {
  preset: "classic", font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 30, maxLines: 12, showPlatform: true, showBadges: true,
};
const meta = {
  presets: { classic, bubble: classic, outline: classic, compact: classic } as Record<OverlayPreset, OverlayStyle>,
  presetOrder: ["classic", "bubble", "outline", "compact"] as OverlayPreset[],
  platforms: ["twitch", "youtube"],
  maxHoldMs: 4000,
};
const KEY = ["pov", "alertskey123456"].join("_");
const overlay = (style: OverlayStyle = classic): ChatOverlay => ({
  id: 7, name: "All", obsKey: KEY, platforms: [], style, createdAt: "", keyRotatedAt: "",
});

const writeText = vi.fn(async () => {});
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText } });
  vi.clearAllMocks();
});
afterEach(cleanup);

const mount = (o = overlay()) =>
  render(<OverlayEditor overlay={o} meta={meta} onChanged={vi.fn()} onDeleted={vi.fn()} />);
const saved = () => (updateOverlay.mock.calls.at(-1) as unknown as [number, { style: OverlayStyle }])[1].style;

describe("the alerts overlay in the overlay editor", () => {
  it("copies the alerts URL (key in the fragment) and the path form", async () => {
    mount();
    fireEvent.click(screen.getByRole("button", { name: T("overlay.copyAlertsUrl") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/overlay/alerts#key=${KEY}`));
    fireEvent.click(screen.getByRole("button", { name: T("overlay.copyAlertsPathUrl") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/overlay/alerts/k/${KEY}`));
    expect(screen.getByText(T("overlay.alertsHint"))).toBeTruthy();
  });

  it("writes NO alerts block when the alerts editor was not touched", async () => {
    mount();
    // the editor shows the defaults ...
    expect((screen.getByLabelText(T("overlay.alertsSeconds")) as HTMLInputElement).value).toBe("6");
    // ... but changing something else saves a style without `alerts`
    fireEvent.change(screen.getByLabelText("Font size (px)"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: T("overlay.save") }));
    await waitFor(() => expect(updateOverlay).toHaveBeenCalled());
    expect("alerts" in saved()).toBe(false);
    expect(saved().fontSize).toBe(30);
  });

  it("writes the whole alerts block, over the defaults, once something in it changes", async () => {
    mount();
    fireEvent.change(screen.getByLabelText(T("overlay.alertsSeconds")), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: T("overlay.save") }));
    await waitFor(() => expect(updateOverlay).toHaveBeenCalled());
    expect(saved().alerts).toEqual({ ...DEFAULT_ALERT_STYLE, seconds: 9 });
  });

  it("clamps the seconds to 3..15", () => {
    mount();
    const seconds = screen.getByLabelText(T("overlay.alertsSeconds")) as HTMLInputElement;
    fireEvent.change(seconds, { target: { value: "99" } });
    expect(seconds.value).toBe("15");
    fireEvent.change(seconds, { target: { value: "1" } });
    expect(seconds.value).toBe("3");
  });

  it("offers exactly the nine overlay kinds and writes the picked subset", async () => {
    mount();
    const group = screen.getByRole("group", { name: T("overlay.alertsKinds") });
    const boxes = within(group).getAllByRole("checkbox");
    expect(boxes).toHaveLength(9);
    expect(within(group).queryByText("Poll")).toBeNull();
    fireEvent.click(within(group).getByRole("checkbox", { name: "Follow" }));
    fireEvent.click(screen.getByRole("button", { name: T("overlay.save") }));
    await waitFor(() => expect(updateOverlay).toHaveBeenCalled());
    expect(saved().alerts!.kinds).toEqual(DEFAULT_ALERT_STYLE.kinds.filter((k) => k !== "follow"));
  });

  it("keeps a stored alerts block when another part of the style changes", async () => {
    const alerts = { ...DEFAULT_ALERT_STYLE, layout: "card" as const, accent: "#00ff00", sound: "bell" as const };
    mount(overlay({ ...classic, alerts }));
    fireEvent.change(screen.getByLabelText("Font size (px)"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: T("overlay.save") }));
    await waitFor(() => expect(updateOverlay).toHaveBeenCalled());
    expect(saved().alerts).toEqual(alerts);
  });

  it("toggles message and amount, and previews the real alert", () => {
    mount();
    const preview = screen.getByTestId("alerts-style");
    expect(within(preview).getByText("pixelpanda sent $5.00")).toBeTruthy();
    expect(within(preview).getByText(T("overlay.alertsPreviewMessage"))).toBeTruthy();
    fireEvent.click(screen.getByRole("switch", { name: T("overlay.alertsShowMessage") }));
    expect(within(preview).queryByText(T("overlay.alertsPreviewMessage"))).toBeNull();
    fireEvent.click(screen.getByRole("switch", { name: T("overlay.alertsShowAmount") }));
    expect(within(preview).getByText("pixelpanda sent a tip")).toBeTruthy();
  });

  it("sends a test alert, defaulting to a tip, and shows the answer", async () => {
    sendTestActivity.mockResolvedValue({ accepted: true, event: {} });
    mount();
    fireEvent.click(screen.getByRole("button", { name: T("activity.sendTest") }));
    await waitFor(() => expect(sendTestActivity).toHaveBeenCalledWith({ kind: "tip" }));
    await waitFor(() => expect(screen.getByText(T("activity.test.accepted"))).toBeTruthy());
  });
});
