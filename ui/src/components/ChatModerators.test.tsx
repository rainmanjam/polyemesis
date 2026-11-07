// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatModerators } from "./ChatModerators";
import { api } from "@/lib/api";
import type { ChatModerator, ChatStatus } from "@/lib/types";

/* YouTube moderator management. The list costs 50 quota units a read, so it is
 * fetched only when the operator presses Load. */

const statuses: ChatStatus[] = [{ platform: "youtube", account: "UC-me", state: "live" } as ChatStatus];
const mod: ChatModerator = { id: "m1", channelId: "UC-m", displayName: "Mo" };

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("the YouTube moderators card", () => {
  it("renders nothing without a YouTube chat", () => {
    const { container } = render(<ChatModerators statuses={[{ platform: "twitch", account: "a", state: "live" } as ChatStatus]} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders one card per connected YouTube account, labelled", async () => {
    const read = vi.spyOn(api, "chatModerators").mockResolvedValue({ moderators: [mod] });
    render(
      <ChatModerators
        statuses={[
          { platform: "youtube", account: "UC-one", state: "live" } as ChatStatus,
          { platform: "youtube", account: "UC-two", state: "live" } as ChatStatus,
        ]}
      />,
    );
    expect(screen.getAllByTestId("chat-moderators")).toHaveLength(2);
    fireEvent.click(screen.getAllByRole("button", { name: "Load moderators" })[1]);
    await waitFor(() => expect(read).toHaveBeenCalledWith({ platform: "youtube", account: "UC-two" }));
    expect(screen.getByText(/UC-two/)).toBeTruthy();
  });

  it("reads nothing until asked, then lists the moderators", async () => {
    const read = vi.spyOn(api, "chatModerators").mockResolvedValue({ moderators: [mod] });
    render(<ChatModerators statuses={statuses} />);
    expect(read).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Load moderators" }));
    await waitFor(() => expect(screen.getByText("Mo")).toBeTruthy());
    expect(read).toHaveBeenCalledWith({ platform: "youtube", account: "UC-me" });
  });

  it("adds by channel id and removes by the moderator id", async () => {
    vi.spyOn(api, "chatModerators").mockResolvedValue({ moderators: [mod] });
    const add = vi.spyOn(api, "addChatModerator").mockResolvedValue({ id: "m2", channelId: "UC-new", displayName: "Newbie" });
    const remove = vi.spyOn(api, "removeChatModerator").mockResolvedValue({ status: "removed" });
    render(<ChatModerators statuses={statuses} />);
    fireEvent.click(screen.getByRole("button", { name: "Load moderators" }));
    await waitFor(() => expect(screen.getByText("Mo")).toBeTruthy());

    const addBtn = screen.getByRole("button", { name: "Add moderator" });
    expect(addBtn).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByLabelText("Channel ID to make a moderator"), { target: { value: " UC-new " } });
    fireEvent.click(addBtn);
    await waitFor(() => expect(screen.getByText("Newbie")).toBeTruthy());
    expect(add).toHaveBeenCalledWith({ platform: "youtube", account: "UC-me", channelId: "UC-new" });

    fireEvent.click(screen.getByRole("button", { name: "Remove Mo as a moderator" }));
    await waitFor(() => expect(screen.queryByText("Mo")).toBeNull());
    expect(remove).toHaveBeenCalledWith({ platform: "youtube", account: "UC-me", id: "m1" });
  });

  it("keeps the list and survives a refusal", async () => {
    vi.spyOn(api, "chatModerators").mockResolvedValue({ moderators: [mod] });
    vi.spyOn(api, "addChatModerator").mockRejectedValue(new Error("Only the owner of the channel can"));
    render(<ChatModerators statuses={statuses} />);
    fireEvent.click(screen.getByRole("button", { name: "Load moderators" }));
    await waitFor(() => expect(screen.getByText("Mo")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Channel ID to make a moderator"), { target: { value: "UC-x" } });
    fireEvent.click(screen.getByRole("button", { name: "Add moderator" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Add moderator" })).toHaveProperty("disabled", false));
    expect(screen.getByText("Mo")).toBeTruthy();
  });
});
