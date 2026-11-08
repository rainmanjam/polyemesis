// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { translate } from "@/lib/i18n";

const reload = vi.fn();
vi.mock("./useActivity", () => ({
  useActivity: () => ({
    events: [], enabled: true, loading: false, forbidden: false, error: "", connected: true,
    frameError: false, reload, replay: vi.fn(),
  }),
}));
vi.mock("./useActivitySources", () => ({
  useActivitySources: () => ({ sources: { enabled: true, kinds: [], platforms: [], sources: [], stats: { received: 0, delivered: 0, invalid: 0, deduped: 0, disabled: 0 } }, failed: false, reload: vi.fn() }),
}));
const sendTestActivity = vi.fn();
const getSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return { ...real, api: { ...real.api, sendTestActivity: (...a: unknown[]) => sendTestActivity(...a), getSettings: (...a: unknown[]) => getSettings(...a) } };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

import { ActivityTab } from "./ActivityTab";

const T = (k: Parameters<typeof translate>[1]) => translate("en", k);

beforeEach(() => {
  vi.clearAllMocks();
  getSettings.mockResolvedValue({ activity: { enabled: true, overlayMessages: true, reactions: false } });
});
afterEach(cleanup);

describe("ActivityTab", () => {
  it("shows the zero-source line and an honest empty feed, and the empty state sends a test", async () => {
    sendTestActivity.mockResolvedValue({ accepted: true, event: {} });
    render(<ActivityTab />);
    expect(screen.getByText(T("activity.sources.none"))).toBeTruthy();
    expect(screen.getByText(T("activity.emptyTitle"))).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: T("activity.sendTest") }));
    await waitFor(() => expect(sendTestActivity).toHaveBeenCalledWith({ kind: "tip" }));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(T("activity.test.accepted")));
  });

  it("toasts a refused test", async () => {
    sendTestActivity.mockResolvedValue({ accepted: false, reason: "disabled", event: {} });
    render(<ActivityTab />);
    fireEvent.click(screen.getByRole("button", { name: T("activity.sendTest") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(T("activity.test.disabled")));
  });

  it("opens the settings in place of the feed, and closes them again", async () => {
    render(<ActivityTab />);
    const toggle = screen.getByRole("button", { name: T("activity.settingsToggle") });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(await screen.findByRole("switch", { name: T("activity.settings.enabled") })).toBeTruthy();
    expect(screen.queryByText(T("activity.emptyTitle"))).toBeNull();
    fireEvent.click(toggle);
    expect(screen.getByText(T("activity.emptyTitle"))).toBeTruthy();
  });
});
