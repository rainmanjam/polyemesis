// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatRules } from "./ChatRules";
import { api } from "@/lib/api";
import type { ChatSettings, ChatStatus } from "@/lib/types";

/* #777: four of the eight rules the server can write had no control.
 *
 * ChatSettings declared emoteMode, followerModeMinutes, nonModeratorChatDelay
 * and nonModeratorChatDelaySeconds; api.updateChatSettings sent every one of
 * them; TwitchAdapter.UpdateChatSettings mapped each onto a Helix field. The
 * panel rendered four switches and there the chain stopped.
 *
 * These are the four worst to be missing. Emote-only and a moderator delay are
 * what a channel reaches for mid-raid, and follower-only with no minimum is a
 * door a raider opens by pressing Follow.
 *
 * Written against what the OPERATOR can reach and what Twitch is then asked
 * for, not against the names on the interface — a test that checks the names
 * is the guard that let this through, since every name was already there.
 */

const update = vi.spyOn(api, "updateChatSettings");

const statuses: ChatStatus[] = [
  { platform: "twitch", account: "acct", state: "live" } as ChatStatus,
];

/** What was PATCHed to Twitch by the last Apply or Turn all off. */
const sent = (): ChatSettings => update.mock.calls[0][2];

beforeEach(() => {
  update.mockReset();
  update.mockResolvedValue({ status: "ok" });
  render(<ChatRules statuses={statuses} />);
});
afterEach(cleanup);

const apply = () => fireEvent.click(screen.getByRole("button", { name: "Apply" }));

describe("the rules a moderator can actually reach", () => {
  it("offers emote-only, and asks Twitch for it", async () => {
    fireEvent.click(screen.getByLabelText("Emotes only"));
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().emoteMode).toBe(true);
  });

  it("offers the moderator delay, and sends the paired duration with it", async () => {
    // Twitch rejects the mode without its duration and names neither field
    // when it does, so the two go together or the request is a dead end.
    fireEvent.click(screen.getByLabelText("Moderator delay"));
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().nonModeratorChatDelay).toBe(true);
    expect(sent().nonModeratorChatDelaySeconds).toBe(4);
  });

  it("lets the delay be changed, and only to a value Helix accepts", async () => {
    fireEvent.click(screen.getByLabelText("Moderator delay"));
    const seconds = screen.getByLabelText(
      "Seconds moderators get to see a message first",
    ) as HTMLSelectElement;
    // A fixed list rather than a number box: 5 is a Bad Request that names no
    // field, on a PATCH also carrying everything else the operator switched on.
    expect([...seconds.options].map((o) => o.value)).toEqual(["2", "4", "6"]);
    fireEvent.change(seconds, { target: { value: "6" } });
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().nonModeratorChatDelaySeconds).toBe(6);
  });

  it("offers the follow age, and sends it with follower-only mode", async () => {
    fireEvent.click(screen.getByLabelText("Followers only"));
    fireEvent.change(screen.getByLabelText("Minutes they must have followed for"), {
      target: { value: "30" },
    });
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().followerMode).toBe(true);
    expect(sent().followerModeMinutes).toBe(30);
  });

  it("hides the follow age until follower-only mode is on", () => {
    // A duration for a mode nobody switched on modifies nothing, which is the
    // rule slow mode already followed.
    expect(screen.queryByLabelText("Minutes they must have followed for")).toBeNull();
  });

  it("keeps zero minutes meaning what it always meant", async () => {
    // Zero is a real Twitch value — "any follower, however new" — and is what
    // follower-only did before this control existed. Switching the mode on and
    // touching nothing else must not change the channel's behaviour.
    fireEvent.click(screen.getByLabelText("Followers only"));
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().followerModeMinutes).toBe(0);
  });

  it("sends nothing for a rule nobody switched on", async () => {
    // An omitted field means "leave it alone" all the way down to Helix, so a
    // stray false here would switch off a mode the operator never touched.
    fireEvent.click(screen.getByLabelText("Emotes only"));
    apply();
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent().nonModeratorChatDelay).toBeUndefined();
    expect(sent().followerMode).toBeUndefined();
  });
});

describe("Turn all off", () => {
  it("turns off all eight, including the four that had no switch", async () => {
    // A button labelled "Turn all off" that leaves emote-only and the
    // moderator delay running is the worst possible answer to its own label:
    // the operator reads the room as reopened and it is not.
    fireEvent.click(screen.getByRole("button", { name: "Turn all off" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(sent()).toMatchObject({
      slowMode: false,
      followerMode: false,
      subscriberMode: false,
      emoteMode: false,
      uniqueChatMode: false,
      nonModeratorChatDelay: false,
    });
  });
});
