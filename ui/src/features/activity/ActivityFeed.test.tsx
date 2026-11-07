// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { translate } from "@/lib/i18n";
import type { ActivityEvent } from "@/lib/types";
import { ActivityFeed, type ActivityFeedProps } from "./ActivityFeed";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

const T = (k: Parameters<typeof translate>[1], p?: Record<string, string | number>) => translate("en", k, p);
const now = Date.now();
const at = (secAgo: number) => new Date(now - secAgo * 1000).toISOString();

const ev = (id: string, over: Partial<ActivityEvent> = {}): ActivityEvent => ({
  id, kind: "follow", platform: "twitch", at: at(5), actor: { name: `user-${id}` }, source: { type: "t", id }, ...over,
});

const base = (over: Partial<ActivityFeedProps> = {}): ActivityFeedProps => ({
  events: [], loading: false, error: "", forbidden: false, enabled: true, connected: true,
  onReplay: vi.fn(async () => "replayed" as const), onSendTest: vi.fn(), ...over,
});

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe("ActivityFeed rows", () => {
  it("draws name, kind, amount, message, relative time and a TEST chip", () => {
    render(
      <ActivityFeed
        {...base({
          events: [
            ev("1", {
              kind: "tip", actor: { name: "Mara" }, amount: { currency: "USD", minor: 500, exponent: 2, display: "$5.00" },
              message: "<b>great</b> stream", test: true, at: at(30),
            }),
            ev("2", { kind: "subscribe", tier: "1000", months: 3, platform: "kick", at: at(120) }),
          ],
        })}
      />,
    );
    const row = document.querySelector('[data-activity-id="1"]') as HTMLElement;
    expect(within(row).getByText("Mara")).toBeTruthy();
    expect(within(row).getByText("Tip")).toBeTruthy();
    expect(within(row).getByText("$5.00")).toBeTruthy();
    expect(within(row).getByText(T("activity.testChip"))).toBeTruthy();
    expect(within(row).getByText(T("activity.ago.s", { n: 30 }))).toBeTruthy();
    // viewer text is text, never markup
    expect(within(row).getByText("<b>great</b> stream")).toBeTruthy();
    expect(row.querySelector("b")).toBeNull();
    const sub = document.querySelector('[data-activity-id="2"]') as HTMLElement;
    expect(within(sub).getByText("Tier 1")).toBeTruthy();
    expect(within(sub).getByText("3 months")).toBeTruthy();
    expect(within(sub).queryByText(T("activity.testChip"))).toBeNull();
    expect(within(sub).getByText(T("activity.ago.m", { n: 2 }))).toBeTruthy();
  });

  it("names a raid by the channel that raided and an anonymous viewer as Someone", () => {
    render(
      <ActivityFeed
        {...base({
          events: [
            ev("r", { kind: "raid", actor: undefined, other: { name: "FriendChannel" }, viewers: 42 }),
            ev("a", { kind: "tip", actor: { name: "", anonymous: true } }),
          ],
        })}
      />,
    );
    expect(screen.getByText("FriendChannel")).toBeTruthy();
    expect(screen.getByText("42 viewers")).toBeTruthy();
    expect(screen.getByText(T("activity.someone"))).toBeTruthy();
  });

  it("offers Replay only for kinds the overlay draws, and reports success", async () => {
    const onReplay = vi.fn(async () => "replayed" as const);
    render(<ActivityFeed {...base({ onReplay, events: [ev("1", { kind: "follow" }), ev("2", { kind: "poll" })] })} />);
    const buttons = screen.getAllByRole("button", { name: T("activity.replay") });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(onReplay).toHaveBeenCalledWith("1"));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(T("activity.replayed")));
  });

  it("tells the operator when the alert is no longer held (404), and when replay fails", async () => {
    const onReplay = vi.fn().mockResolvedValueOnce("gone").mockRejectedValueOnce(new Error("boom")).mockRejectedValueOnce({});
    render(<ActivityFeed {...base({ onReplay, events: [ev("1")] })} />);
    const btn = () => screen.getByRole("button", { name: T("activity.replay") });
    fireEvent.click(btn());
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(T("activity.replayGone")));
    fireEvent.click(btn());
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("boom"));
    fireEvent.click(btn());
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(T("activity.replayFailed")));
  });
});

describe("ActivityFeed filters", () => {
  const events = [
    ev("1", { platform: "twitch", kind: "follow" }),
    ev("2", { platform: "kick", kind: "tip", amount: { currency: "USD", minor: 300, exponent: 2, display: "$3.00" } }),
    ev("3", { platform: "kick", kind: "tip", amount: { currency: "USD", minor: 1500, exponent: 2, display: "$15.00" } }),
    ev("4", { platform: "twitch", kind: "tip", amount: { unit: "bits", units: 50, display: "50 bits" } }),
  ];
  const rows = () => Array.from(document.querySelectorAll("[data-activity-id]")).map((r) => r.getAttribute("data-activity-id"));

  it("hides a platform and a kind with toggle chips, and says how many are shown", () => {
    render(<ActivityFeed {...base({ events })} />);
    expect(rows()).toEqual(["1", "2", "3", "4"]);
    const platforms = screen.getByRole("group", { name: T("activity.filterPlatforms") });
    const kick = within(platforms).getByRole("button", { name: /Kick/i });
    expect(kick.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(kick);
    expect(kick.getAttribute("aria-pressed")).toBe("false");
    expect(rows()).toEqual(["1", "4"]);
    expect(screen.getByText("2 of 4")).toBeTruthy();
    const kinds = screen.getByRole("group", { name: T("activity.filterKinds") });
    fireEvent.click(within(kinds).getByRole("button", { name: /Follow/ }));
    expect(rows()).toEqual(["4"]);
    fireEvent.click(kick);
    expect(rows()).toEqual(["2", "3", "4"]);
  });

  it("hides amounts below a minimum in the SAME currency only, view only", () => {
    render(<ActivityFeed {...base({ events })} />);
    const code = screen.getByRole("combobox", { name: T("activity.hideBelowCode") });
    const amount = screen.getByRole("spinbutton", { name: T("activity.hideBelowAmount") }) as HTMLInputElement;
    expect(amount.disabled).toBe(true);
    fireEvent.change(code, { target: { value: "USD" } });
    fireEvent.change(amount, { target: { value: "5" } });
    // $3.00 is hidden; $15.00 stays; 50 bits (another unit) is never compared; the follow has no amount
    expect(rows()).toEqual(["1", "3", "4"]);
    fireEvent.change(code, { target: { value: "bits" } });
    fireEvent.change(amount, { target: { value: "100" } });
    expect(rows()).toEqual(["1", "2", "3"]);
    fireEvent.change(code, { target: { value: "" } });
    expect(rows()).toEqual(["1", "2", "3", "4"]);
  });
});

describe("ActivityFeed empty states", () => {
  it("explains where activity comes from and offers a test alert", () => {
    const onSendTest = vi.fn();
    render(<ActivityFeed {...base({ onSendTest })} />);
    expect(screen.getByText(T("activity.emptyTitle"))).toBeTruthy();
    expect(screen.getByText(T("activity.emptyBody"))).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: T("activity.sendTest") }));
    expect(onSendTest).toHaveBeenCalled();
  });

  it("says so when activity is switched off", () => {
    render(<ActivityFeed {...base({ enabled: false })} />);
    expect(screen.getByText(T("activity.emptyOff"))).toBeTruthy();
  });

  it("says everything is filtered out, rather than that nothing happened", () => {
    render(<ActivityFeed {...base({ events: [ev("1")] })} />);
    fireEvent.click(screen.getByRole("button", { name: /Twitch/i }));
    expect(screen.getByText(T("activity.emptyFilteredTitle"))).toBeTruthy();
  });

  it("shows loading, a refusal for a read-scoped session, and an error", () => {
    const { rerender } = render(<ActivityFeed {...base({ loading: true })} />);
    expect(screen.getByText(T("activity.loading"))).toBeTruthy();
    rerender(<ActivityFeed {...base({ forbidden: true })} />);
    expect(screen.getByText(T("activity.forbidden"))).toBeTruthy();
    rerender(<ActivityFeed {...base({ error: "boom" })} />);
    expect(screen.getByRole("alert").textContent).toContain("boom");
  });

  it("flags a dropped live connection", () => {
    render(<ActivityFeed {...base({ connected: false, events: [ev("1")] })} />);
    expect(screen.getByText(T("chatpage.socketOffline"))).toBeTruthy();
  });
});
