// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AutomodProtectionTwitch } from "./AutomodProtectionTwitch";
import { api, ApiError } from "@/lib/api";
import type { ChatOverview, TwitchPackStatus, TwitchPackSync } from "@/lib/types";

/* TWITCH'S OWN FILTERS: every action is a click, the term sync is re-run until
 * the server says it is done, and nothing on the card is a term. */

const overview = vi.spyOn(api.twitchTool, "read");
const post = vi.spyOn(api.twitchTool, "post");
const put = vi.spyOn(api.twitchTool, "put");
const remove = vi.spyOn(api.twitchTool, "remove");
const chatOverview = vi.spyOn(api, "chatOverview");

function status(over: Partial<TwitchPackStatus> = {}): TwitchPackStatus {
  return { eligible: 80, recorded: 0, pushedPackVersion: 0, packVersion: 1, on: true, ...over };
}

function sync(over: Partial<TwitchPackSync> = {}): TwitchPackSync {
  return { added: 0, present: 0, removed: 0, skipped: 0, remaining: 0, done: true, ...over };
}

function chat(account: string | null): ChatOverview {
  return {
    configured: true,
    messages: [],
    limits: [],
    statuses: account === null ? [] : [{ platform: "twitch", account, state: "live", since: "", received: 0, sent: 0, restarts: 0 }],
  } as unknown as ChatOverview;
}

beforeEach(() => {
  chatOverview.mockResolvedValue(chat("acct"));
  overview.mockResolvedValue(status());
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("AutomodProtectionTwitch", () => {
  it("renders nothing without a Twitch connection, or when the chat read fails", async () => {
    chatOverview.mockResolvedValue(chat(null));
    const { container } = render(<AutomodProtectionTwitch />);
    await waitFor(() => expect(chatOverview).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    cleanup();
    chatOverview.mockRejectedValue(new Error("down"));
    const again = render(<AutomodProtectionTwitch />);
    await waitFor(() => expect(chatOverview).toHaveBeenCalledTimes(2));
    expect(again.container.textContent).toBe("");
    expect(overview).not.toHaveBeenCalled();
  });

  it("shows how many terms would be sent and runs nothing until clicked", async () => {
    render(<AutomodProtectionTwitch />);
    expect(await screen.findByText(/Sends 80 terms/)).toBeTruthy();
    expect(overview).toHaveBeenCalledWith("pack-terms", "acct");
    expect(post).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
    // No removal button while nothing of ours is on Twitch.
    expect(screen.queryByText("Remove the terms polyemesis added")).toBeNull();
  });

  it("re-runs the term sync until the server says it is done and adds the passes up", async () => {
    post
      .mockResolvedValueOnce(sync({ added: 30, remaining: 50, done: false }))
      .mockResolvedValueOnce(sync({ added: 30, remaining: 20, done: false }))
      .mockResolvedValueOnce(sync({ added: 20, present: 2, skipped: 1, remaining: 0, done: true }));
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }));
    expect(await screen.findByText("Done: 80 added, 2 already there, 1 skipped, 0 still to go.")).toBeTruthy();
    expect(post).toHaveBeenCalledTimes(3);
    expect(post).toHaveBeenCalledWith("pack-terms-sync", "acct", {});
  });

  it("stops looping when the remaining count stops falling, and reports it as unfinished", async () => {
    post.mockResolvedValue(sync({ remaining: 12, done: false }));
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Not finished: 12 still to go");
    expect(screen.queryByText(/^Done:/)).toBeNull();
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("keeps going through passes that added nothing but still made progress", async () => {
    // Every add in a pass came back "present": added is 0, remaining still falls.
    post
      .mockResolvedValueOnce(sync({ present: 30, remaining: 50, done: false }))
      .mockResolvedValueOnce(sync({ present: 30, remaining: 20, done: false }))
      .mockResolvedValueOnce(sync({ present: 20, remaining: 0, done: true }));
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }));
    expect(await screen.findByText(/^Done: 0 added/)).toBeTruthy();
    expect(post).toHaveBeenCalledTimes(3);
  });

  it("will not copy while the card holds unsaved protection edits, and re-reads the count after a save", async () => {
    const { rerender } = render(<AutomodProtectionTwitch unsaved savedKey="a" />);
    await screen.findByText(/Sends 80 terms/);
    expect((screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Save your protection changes first/)).toBeTruthy();
    const reads = overview.mock.calls.length;
    rerender(<AutomodProtectionTwitch savedKey="b" />);
    await waitFor(() => expect(overview.mock.calls.length).toBeGreaterThan(reads));
    expect((screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows Twitch's reason when a pass fails", async () => {
    post.mockRejectedValue(new ApiError(400, "stopped after adding 3 of 9"));
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Twitch did not accept that");
    expect(alert.textContent).toContain("stopped after adding 3 of 9");
  });

  it("offers the reversal only once polyemesis has added terms, and reports the count", async () => {
    overview.mockResolvedValue(status({ recorded: 80, pushedPackVersion: 1 }));
    remove.mockResolvedValue(sync({ removed: 80 }));
    render(<AutomodProtectionTwitch />);
    expect(await screen.findByText(/Added by polyemesis so far: 80/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove the terms polyemesis added" }));
    expect(await screen.findByText(/Removed 80; 0 still to go/)).toBeTruthy();
    expect(remove).toHaveBeenCalledWith("pack-terms", "acct", {});
  });

  it("raises AutoMod to the chosen level", async () => {
    put.mockResolvedValue({});
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Set Twitch AutoMod to level 2" }));
    expect(await screen.findByText(/level 2 or stricter/)).toBeTruthy();
    expect(put).toHaveBeenCalledWith("pack-automod-level", "acct", { level: 2 });
    fireEvent.click(screen.getByRole("button", { name: "Set Twitch AutoMod to level 1" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("pack-automod-level", "acct", { level: 1 }));
  });

  it("refreshes the protected names and passes on why the VIPs could not be read", async () => {
    post.mockResolvedValue({ moderators: 3, vips: 0, vipNote: "needs more" });
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    fireEvent.click(screen.getByRole("button", { name: "Refresh protected names from Twitch roles" }));
    const note = await screen.findByRole("status");
    expect(note.textContent).toContain("3 moderators and 0 VIPs");
    expect(note.textContent).toContain("VIPs could not be read: needs more");
    expect(post).toHaveBeenCalledWith("pack-protected-names", "acct", {});
  });

  it("disables the copy button while protection is off", async () => {
    overview.mockResolvedValue(status({ on: false }));
    render(<AutomodProtectionTwitch />);
    await screen.findByText(/Sends 80 terms/);
    expect((screen.getByRole("button", { name: "Copy the severe hate terms to Twitch blocked terms" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
