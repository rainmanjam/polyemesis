// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { translate } from "@/lib/i18n";
import type { ActivitySourcesResponse, Settings } from "@/lib/types";

const getSettings = vi.fn();
const putSettings = vi.fn();
const sendTestActivity = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: {
      ...real.api,
      getSettings: (...a: unknown[]) => getSettings(...a),
      putSettings: (...a: unknown[]) => putSettings(...a),
      sendTestActivity: (...a: unknown[]) => sendTestActivity(...a),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

import { ApiError } from "@/lib/api";
import { ActivitySettings } from "./ActivitySettings";

const T = (k: Parameters<typeof translate>[1], p?: Record<string, string | number>) => translate("en", k, p);
const kindSwitch = (kind: string, platform: string) =>
  screen.getByRole("switch", { name: T("activity.settings.kindAria", { kind, platform }) });

const doc = (activity?: Settings["activity"]) => ({ version: "v1", other: "kept", ...(activity ? { activity } : {}) }) as unknown as Settings;

beforeEach(() => {
  vi.clearAllMocks();
  getSettings.mockResolvedValue(doc({ enabled: true, overlayMessages: true, reactions: false }));
  putSettings.mockImplementation(async (s: Settings) => ({ ...s, version: "v2" }));
});
afterEach(cleanup);

async function ready(props: Parameters<typeof ActivitySettings>[0] = {}) {
  const r = render(<ActivitySettings {...props} />);
  await screen.findByRole("switch", { name: T("activity.settings.enabled") });
  return r;
}
const save = () => fireEvent.click(screen.getByRole("button", { name: T("activity.settings.save") }));
const body = () => putSettings.mock.calls.at(-1)![0] as Settings;

describe("ActivitySettings", () => {
  it("lists every kind for every platform even with no source reporting", async () => {
    await ready();
    for (const p of ["Twitch", "YouTube", "Kick", "X", "Rumble", "Facebook"]) {
      expect(screen.getByRole("group", { name: new RegExp(`^${p}`) })).toBeTruthy();
    }
    expect(screen.getAllByRole("switch", { name: /^Raid on / })).toHaveLength(6);
    expect(screen.getAllByRole("switch", { name: /^Reaction on / })).toHaveLength(6);
    // defaults: everything but reactions
    expect(kindSwitch("Tip", "Twitch").getAttribute("aria-checked")).toBe("true");
    expect(kindSwitch("Reaction", "Facebook").getAttribute("aria-checked")).toBe("false");
    // non-overlay kinds are marked as console only
    const twitch = screen.getByRole("group", { name: /^Twitch/ });
    expect(within(twitch).getAllByText(/console only/).length).toBe(5);
    // nothing to save until something changes
    expect((screen.getByRole("button", { name: T("activity.settings.save") }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("uses the platforms and kinds the server names, and greys the reported reason without hiding the kind", async () => {
    const sources: ActivitySourcesResponse = {
      enabled: true,
      platforms: ["twitch"],
      kinds: [{ kind: "tip", overlay: true }, { kind: "poll", overlay: false }, { kind: "raid", overlay: true }],
      stats: { received: 0, delivered: 0, invalid: 0, deduped: 0, disabled: 0 },
      sources: [{
        platform: "twitch", state: "live", transport: "own", events: 0,
        kinds: [{ kind: "tip", state: "on" }, { kind: "raid", state: "unavailable", detail: "needs Affiliate" }, { kind: "poll", state: "reconnect", detail: "reconnect to grant" }],
      }],
    };
    await ready({ sources });
    expect(screen.getAllByRole("group", { name: /Twitch/ })).toHaveLength(1);
    expect(screen.queryByRole("group", { name: /Kick/ })).toBeNull();
    expect(screen.getAllByRole("switch", { name: /on Twitch$/ })).toHaveLength(3);
    const twitch = screen.getByRole("group", { name: /^Twitch/ });
    expect(within(twitch).getByText("Unavailable: needs Affiliate")).toBeTruthy();
    expect(within(twitch).getByText("Reconnect needed: reconnect to grant")).toBeTruthy();
    expect(within(twitch).getByText("(console only)")).toBeTruthy();
    // still operable
    expect((kindSwitch("Raid", "Twitch") as HTMLButtonElement).disabled).toBe(false);
  });

  it("saves the whole document with the activity block replaced and the rest kept", async () => {
    await ready();
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.reactions") }));
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.overlayMessages") }));
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.enabled") }));
    save();
    await waitFor(() => expect(putSettings).toHaveBeenCalledTimes(1));
    const sent = body() as unknown as Record<string, unknown>;
    expect(sent.other).toBe("kept");
    expect(sent.version).toBe("v1"); // the version check travels with it
    expect(sent.activity).toEqual({ enabled: false, overlayMessages: false, reactions: true });
    await screen.findByText(T("save.saved"));
  });

  it("writes an explicit list for a platform on its first toggle, and removes the key on Back to defaults", async () => {
    await ready();
    expect(screen.queryByRole("button", { name: T("activity.settings.resetDefaults") })).toBeNull();
    fireEvent.click(kindSwitch("Follow", "Kick"));
    save();
    await waitFor(() => expect(putSettings).toHaveBeenCalledTimes(1));
    const kinds = (body().activity as { kinds: Record<string, string[]> }).kinds;
    expect(Object.keys(kinds)).toEqual(["kick"]);
    expect(kinds.kick).not.toContain("follow");
    expect(kinds.kick).toContain("tip");
    expect(kinds.kick).not.toContain("reaction");

    // the server stored it; now put the platform back on defaults
    await screen.findByText(T("save.saved"));
    const kick = screen.getByRole("group", { name: /^Kick/ });
    fireEvent.click(within(kick).getByRole("button", { name: T("activity.settings.resetDefaults") }));
    expect(kindSwitch("Follow", "Kick").getAttribute("aria-checked")).toBe("true");
    save();
    await waitFor(() => expect(putSettings).toHaveBeenCalledTimes(2));
    // kinds REPLACES wholesale when sent, so the emptied map is sent as {}
    expect((body().activity as { kinds?: unknown }).kinds).toEqual({});
  });

  it("keeps [] as an explicit all-off platform", async () => {
    getSettings.mockResolvedValue(doc({ enabled: true, overlayMessages: true, reactions: false, kinds: { twitch: [], kick: ["tip"] } }));
    await ready();
    expect(kindSwitch("Tip", "Twitch").getAttribute("aria-checked")).toBe("false");
    expect(kindSwitch("Tip", "Kick").getAttribute("aria-checked")).toBe("true");
    expect(kindSwitch("Follow", "Kick").getAttribute("aria-checked")).toBe("false");
    expect(kindSwitch("Tip", "YouTube").getAttribute("aria-checked")).toBe("true");
  });

  it("edits minimum amounts per currency and unit, says amounts are not converted, and normalises on save", async () => {
    await ready();
    expect(screen.getByText(/never converted between currencies/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: T("activity.settings.addMoney") }));
    fireEvent.click(screen.getByRole("button", { name: T("activity.settings.addUnit") }));
    const save_ = screen.getByRole("button", { name: T("activity.settings.save") }) as HTMLButtonElement;
    // an empty row is a problem, said next to the disabled button
    expect(save_.disabled).toBe(true);
    expect(screen.getByText(T("activity.settings.minBadCurrency"))).toBeTruthy();
    fireEvent.change(document.getElementById("activity-min-0-currency")!, { target: { value: "usd" } });
    fireEvent.change(document.getElementById("activity-min-0-minor")!, { target: { value: "500" } });
    expect(screen.getByText(T("activity.settings.minBadUnit"))).toBeTruthy();
    fireEvent.change(document.getElementById("activity-min-1-unit")!, { target: { value: "BITS" } });
    fireEvent.change(document.getElementById("activity-min-1-units")!, { target: { value: "100" } });
    expect(save_.disabled).toBe(false);
    save();
    await waitFor(() => expect(putSettings).toHaveBeenCalled());
    expect((body().activity as { minTip: unknown }).minTip).toEqual([
      { currency: "USD", minor: 500 },
      { unit: "bits", units: 100 },
    ]);
  });

  it("removes a minimum row, and clears the stored list with []", async () => {
    getSettings.mockResolvedValue(doc({ enabled: true, overlayMessages: true, reactions: false, minTip: [{ currency: "EUR", minor: 200 }] }));
    await ready();
    expect((document.getElementById("activity-min-0-currency") as HTMLInputElement).value).toBe("EUR");
    fireEvent.click(screen.getByRole("button", { name: T("activity.settings.minRemove") }));
    save();
    await waitFor(() => expect(putSettings).toHaveBeenCalled());
    expect((body().activity as { minTip: unknown }).minTip).toEqual([]);
  });

  it("offers the conflict reload instead of a retry when somebody saved first", async () => {
    putSettings.mockRejectedValue(new ApiError(409, "settings changed elsewhere", "settings_conflict"));
    await ready();
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.reactions") }));
    save();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    const [msg, opts] = (toast.error as unknown as { mock: { calls: [string, { action?: { label: string } }][] } }).mock.calls[0];
    expect(msg).toBe("settings changed elsewhere");
    expect(opts.action?.label).toBe(T("chatpage.reload"));
    // the draft stays, so nothing typed is lost
    expect(screen.getByRole("switch", { name: T("activity.settings.reactions") }).getAttribute("aria-checked")).toBe("true");
  });

  it("toasts any other save failure and keeps the draft dirty", async () => {
    putSettings.mockRejectedValue(new ApiError(500, "disk full"));
    await ready();
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.reactions") }));
    save();
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("disk full"));
    await screen.findByText(T("save.failed"));
  });

  it("says when the settings could not be read", async () => {
    getSettings.mockRejectedValue(new Error("nope"));
    render(<ActivitySettings />);
    expect((await screen.findByRole("alert")).textContent).toBe(T("activity.settings.loadFailed"));
  });

  it("calls onSaved after a save", async () => {
    const onSaved = vi.fn();
    await ready({ onSaved });
    fireEvent.click(screen.getByRole("switch", { name: T("activity.settings.reactions") }));
    save();
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });
});

describe("ActivitySettings: Send test alert", () => {
  const send = () => fireEvent.click(screen.getByRole("button", { name: T("activity.sendTest") }));
  const status = () => screen.getAllByRole("status").find((e) => e.hasAttribute("data-test-ok") || e.className.includes("text-sm"))!;

  it("posts the picked kind (default tip) and shows that it was accepted", async () => {
    sendTestActivity.mockResolvedValue({ accepted: true, event: {} });
    await ready();
    send();
    await waitFor(() => expect(sendTestActivity).toHaveBeenCalledWith({ kind: "tip" }));
    await waitFor(() => expect(document.querySelector('[data-test-ok="true"]')?.textContent).toBe(T("activity.test.accepted")));
    expect(status()).toBeTruthy();
  });

  it("says why a test was not delivered", async () => {
    sendTestActivity.mockResolvedValueOnce({ accepted: false, reason: "disabled", detail: "Turn activity on.", event: {} });
    await ready();
    send();
    await waitFor(() => expect(document.querySelector('[data-test-ok="false"]')?.textContent).toBe(`${T("activity.test.disabled")} Turn activity on.`));
    sendTestActivity.mockResolvedValueOnce({ accepted: false, reason: "duplicate", event: {} });
    send();
    await waitFor(() => expect(document.querySelector('[data-test-ok="false"]')?.textContent).toBe(T("activity.test.duplicate")));
  });

  it("shows the server's sentence for a 429, and a plain one for a failure with no message", async () => {
    sendTestActivity.mockRejectedValueOnce(new ApiError(429, "slow down: 2 test alerts per second"));
    await ready();
    send();
    await waitFor(() => expect(document.querySelector('[data-test-ok="false"]')?.textContent).toBe("slow down: 2 test alerts per second"));
    sendTestActivity.mockRejectedValueOnce("???");
    send();
    await waitFor(() => expect(document.querySelector('[data-test-ok="false"]')?.textContent).toBe(T("activity.test.failed")));
  });
});
