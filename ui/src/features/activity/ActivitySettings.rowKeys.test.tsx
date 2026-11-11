// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { translate } from "@/lib/i18n";
import type { Settings } from "@/lib/types";

// The minimum-tip rows are editable and carry no id. Their React keys must
// follow the row, so removing a middle row must not hand its DOM (and typed
// state) to the row below it. These tests watch the DOM node itself.

const getSettings = vi.fn();
const putSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: {
      ...real.api,
      getSettings: (...a: unknown[]) => getSettings(...a),
      putSettings: (...a: unknown[]) => putSettings(...a),
      sendTestActivity: vi.fn(),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

import { ActivitySettings } from "./ActivitySettings";

const T = (k: Parameters<typeof translate>[1]) => translate("en", k);

const doc = (activity: Settings["activity"]) =>
  ({ version: "v1", other: "kept", activity }) as unknown as Settings;

const minTip = [
  { currency: "USD", minor: 100 },
  { unit: "bits", units: 5 },
  { currency: "EUR", minor: 200 },
];

beforeEach(() => {
  vi.clearAllMocks();
  getSettings.mockResolvedValue(doc({ enabled: true, overlayMessages: true, reactions: false, minTip }));
  putSettings.mockImplementation(async (s: Settings) => ({ ...s, version: "v2" }));
});
afterEach(cleanup);

async function ready() {
  render(<ActivitySettings />);
  await screen.findByRole("switch", { name: T("activity.settings.enabled") });
}

const removeButtons = () => screen.getAllByRole("button", { name: T("activity.settings.minRemove") });

describe("ActivitySettings minimum-tip rows", () => {
  it("keeps the other rows' DOM when a middle row is removed", async () => {
    await ready();
    const eurInput = screen.getByDisplayValue("EUR");
    const usdInput = screen.getByDisplayValue("USD");

    fireEvent.click(removeButtons()[1]);

    expect(screen.queryByDisplayValue("bits")).toBeNull();
    expect(screen.getByDisplayValue("EUR")).toBe(eurInput);
    expect(screen.getByDisplayValue("USD")).toBe(usdInput);
  });

  it("keeps a row's DOM while it is being typed into", async () => {
    await ready();
    const eurInput = screen.getByDisplayValue("EUR");

    fireEvent.change(screen.getByDisplayValue("USD"), { target: { value: "GBP" } });

    expect(screen.getByDisplayValue("EUR")).toBe(eurInput);
    expect(screen.getByDisplayValue("GBP")).toBeTruthy();
  });

  it("does not send the row keys to the API", async () => {
    await ready();
    fireEvent.click(removeButtons()[1]);
    fireEvent.click(screen.getByRole("button", { name: T("activity.settings.save") }));

    await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
    const sent = putSettings.mock.calls.at(-1)![0] as Settings;
    expect(sent.activity?.minTip).toEqual([
      { currency: "USD", minor: 100 },
      { currency: "EUR", minor: 200 },
    ]);
  });
});
