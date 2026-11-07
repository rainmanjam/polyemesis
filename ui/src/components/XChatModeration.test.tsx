// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { XChatModeration } from "./XChatModeration";
import { api } from "@/lib/api";
import type { ChatStatus } from "@/lib/types";

const mutes = vi.spyOn(api, "getChatMutes");
const mods = vi.spyOn(api, "getChatModerators");
const addMod = vi.spyOn(api, "addChatModerator");
const removeMod = vi.spyOn(api, "removeChatModerator");
const unban = vi.spyOn(api, "unbanChatUser");

const statuses: ChatStatus[] = [{ platform: "x", account: "acct", state: "live" } as ChatStatus];

beforeEach(() => {
  for (const m of [mutes, mods, addMod, removeMod, unban]) m.mockReset();
  mutes.mockResolvedValue({
    mutes: [
      { userId: "11", username: "loud", name: "Loud One", endsAt: "2026-11-08T10:00:00Z" },
      { userId: "22", username: "quiet" },
    ],
  });
  mods.mockResolvedValue({ moderators: [{ userId: "777", username: "helper" }], appliesToAllBroadcasts: true });
});
afterEach(cleanup);

describe("X muted viewers and moderators", () => {
  it("renders nothing without a connected X chat", () => {
    const { container } = render(<XChatModeration statuses={[]} />);
    expect(container.innerHTML).toBe("");
    expect(mutes).not.toHaveBeenCalled();
  });

  it("shows each mute with its expiry or 'until unmuted', and says moderators act across every broadcast", async () => {
    render(<XChatModeration statuses={statuses} />);
    await screen.findByText(/Loud One \(@loud\)/);
    expect(screen.getByText(/until unmuted/)).toBeTruthy();
    expect(screen.getByText(/^until .*2026/)).toBeTruthy();
    expect(screen.getByText(/every broadcast this account hosts/)).toBeTruthy();
    expect(screen.getByText("@helper")).toBeTruthy();
  });

  it("unmutes through the ban route's counterpart and reloads", async () => {
    unban.mockResolvedValue({ status: "unbanned" });
    render(<XChatModeration statuses={statuses} />);
    await screen.findByText(/@quiet/);
    fireEvent.click(screen.getAllByRole("button", { name: "Unmute" })[1]);
    await waitFor(() => expect(unban).toHaveBeenCalledWith({ platform: "x", account: "acct", userId: "22" }));
    await waitFor(() => expect(mutes).toHaveBeenCalledTimes(2));
  });

  it("adds a moderator only for a numeric id, and removes one", async () => {
    addMod.mockResolvedValue({ ids: ["777", "888"] });
    removeMod.mockResolvedValue({ ids: [] });
    render(<XChatModeration statuses={statuses} />);
    await screen.findByText("@helper");
    const input = screen.getByLabelText("Add a moderator by X user id");
    const add = screen.getByRole("button", { name: "Add" });
    fireEvent.change(input, { target: { value: "not-an-id" } });
    expect(add).toHaveProperty("disabled", true);
    expect(screen.getByText("An X user id is digits only.")).toBeTruthy();
    fireEvent.change(input, { target: { value: "888" } });
    fireEvent.click(add);
    await waitFor(() => expect(addMod).toHaveBeenCalledWith("acct", "888"));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(removeMod).toHaveBeenCalledWith("acct", "777"));
  });

  it("reports a failed read in X's words instead of an empty list", async () => {
    mutes.mockRejectedValue(new Error("only the broadcast owner can list mutes"));
    render(<XChatModeration statuses={statuses} />);
    await screen.findByText(/only the broadcast owner can list mutes/);
    expect(screen.queryByText("Nobody is muted.")).toBeNull();
  });

  it("says when X holds more mutes than the list shows", async () => {
    mutes.mockResolvedValue({ mutes: [{ userId: "11", username: "loud" }], truncated: true });
    render(<XChatModeration statuses={statuses} />);
    await screen.findByText(/more muted viewers than this list shows/);
  });

  it("shows a card for every connected X account, each reading its own lists", async () => {
    render(
      <XChatModeration
        statuses={[
          { platform: "x", account: "one", state: "live" } as ChatStatus,
          { platform: "x", account: "two", state: "live" } as ChatStatus,
        ]}
      />,
    );
    await waitFor(() => expect(mutes).toHaveBeenCalledWith("one"));
    await waitFor(() => expect(mutes).toHaveBeenCalledWith("two"));
    expect(screen.getAllByRole("region").length).toBe(2);
  });
});
