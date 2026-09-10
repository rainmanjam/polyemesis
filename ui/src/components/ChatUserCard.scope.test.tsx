// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import { moderationColumnSaysYes } from "@/lib/chatModeration";
import type { ChatPlatform, ChatUserCard as CardData } from "@/lib/types";

/* #769 and #783, which are the same defect twice.
 *
 * #769: every control on this card was gated on ONE value —
 * `supportOf(capabilityFor(platform), "moderation") === "yes"`. That is a
 * column in the setup page's capability matrix and a summary of the whole
 * platform. Facebook answers yes to it, correctly, because it can delete and
 * hide a comment; FacebookAdapter implements no chat.Banner at all, so
 * Hub.Ban refuses before it reaches the network. Four timeout buttons and a
 * permanent ban rendered live and enabled, and every press produced an error
 * toast. A control that lies to a moderator is worse than one that is missing.
 *
 * #783: api.hideChatMessage has taken `scope: "local" | "platform"` since it
 * was written, and its only caller passed the literal "local". Facebook's
 * upstream hide — reversible, and the one power Facebook has that no other
 * platform here does — was unreachable from the UI.
 *
 * jsdom because Radix renders the dialog through a portal.
 */

const chatUser = vi.spyOn(api, "chatUser");
const hide = vi.spyOn(api, "hideChatMessage");

const card = (over: Partial<CardData> = {}): CardData =>
  ({
    name: "someone",
    messages: [
      {
        id: "m1",
        text: "hi",
        at: "2026-08-20T12:00:00Z",
        platform: "facebook",
        author: { id: "u1", name: "someone" },
      },
    ],
    ...over,
  }) as CardData;

function draw(platform: ChatPlatform) {
  return render(
    <ChatUserCard
      platform={platform}
      authorId="u1"
      authorName="someone"
      open
      onOpenChange={() => {}}
    />,
  );
}

const button = (name: RegExp) => screen.getByRole("button", { name }) as HTMLButtonElement;

beforeEach(() => {
  chatUser.mockReset();
  hide.mockReset();
  hide.mockResolvedValue({} as Awaited<ReturnType<typeof api.hideChatMessage>>);
  chatUser.mockResolvedValue(card());
});
afterEach(cleanup);

describe("#769: a platform whose moderation column says yes and whose ban says no", () => {
  it("is a platform whose column really does say yes", () => {
    // Pins the premise, so this whole file cannot quietly stop testing the
    // case it was written for.
    expect(moderationColumnSaysYes("facebook")).toBe(true);
  });

  it("renders every timeout inert rather than enabled and always failing", async () => {
    draw("facebook");
    await waitFor(() => expect(button(/1 min/).disabled).toBe(true));
    expect(button(/1 day/).disabled).toBe(true);
  });

  it("renders the permanent ban and the lift inert too", async () => {
    draw("facebook");
    await waitFor(() => expect(button(/Ban permanently/).disabled).toBe(true));
    expect(button(/Lift ban/).disabled).toBe(true);
  });

  it("says why, naming what the moderator can do instead", async () => {
    draw("facebook");
    await waitFor(() => expect(screen.getByText(/no chat ban API/)).toBeTruthy());
    expect(screen.getByText(/Facebook page itself/)).toBeTruthy();
  });

  it("leaves the hides live, because Facebook can genuinely hide a comment", async () => {
    // The other half. One flag for the whole card cannot express this, which
    // is why the sentence above sits beside the ban row rather than at the
    // foot of the dialog claiming polyemesis cannot moderate Facebook at all.
    draw("facebook");
    await waitFor(() => expect(button(/Hide from viewers/).disabled).toBe(false));
    expect(button(/Hide here only/).disabled).toBe(false);
  });

  it("leaves a platform that can do everything alone", async () => {
    draw("twitch");
    await waitFor(() => expect(button(/1 min/).disabled).toBe(false));
    expect(button(/Ban permanently/).disabled).toBe(false);
    expect(screen.queryByText(/no chat ban API/)).toBeNull();
  });
});

describe("#783: hiding from viewers, which no operator could ask for", () => {
  it("asks for the platform scope, not the local one", async () => {
    draw("facebook");
    await waitFor(() => expect(button(/Hide from viewers/).disabled).toBe(false));
    fireEvent.click(button(/Hide from viewers/));
    await waitFor(() => expect(hide).toHaveBeenCalled());
    expect(hide.mock.calls[0][0]).toMatchObject({ scope: "platform", hidden: true });
  });

  it("still offers the local hide, at the local scope", async () => {
    // The two are one endpoint and two different promises to the audience.
    // Collapsing them back into one button is the defect, not the fix.
    draw("facebook");
    await waitFor(() => expect(button(/Hide here only/).disabled).toBe(false));
    fireEvent.click(button(/Hide here only/));
    await waitFor(() => expect(hide).toHaveBeenCalled());
    expect(hide.mock.calls[0][0]).toMatchObject({ scope: "local" });
  });

  it("offers the undo as a button, not as a claim", async () => {
    // "Hiding is reversible" is worth nothing to a moderator choosing between
    // hide and delete under time pressure unless something reverses it.
    draw("facebook");
    await waitFor(() => expect(button(/Show to viewers again/).disabled).toBe(false));
    fireEvent.click(button(/Show to viewers again/));
    await waitFor(() => expect(hide).toHaveBeenCalled());
    expect(hide.mock.calls[0][0]).toMatchObject({ scope: "platform", hidden: false });
  });

  it("makes the difference between hiding and deleting legible", async () => {
    draw("facebook");
    await waitFor(() => expect(screen.getByText(/Hiding can be undone/)).toBeTruthy());
  });

  it("renders the upstream hide inert with its reason where it cannot work", async () => {
    // Inert and explained, never absent: a control that comes and goes as you
    // click between platforms teaches nobody what the platforms differ on.
    draw("twitch");
    // WAIT FOR THE LOAD FIRST. Every hide is disabled while the scrollback is
    // still arriving, for the ordinary reason that there is nothing on record
    // yet — so asserting straight away passes without testing the gate at all.
    await waitFor(() => expect(button(/Hide here only/).disabled).toBe(false));
    expect(button(/Hide from viewers/).disabled).toBe(true);
    expect(button(/Show to viewers again/).disabled).toBe(true);
    expect(screen.getByText(/Only Facebook can hide a message upstream/)).toBeTruthy();
  });

  it("never asks the platform to hide messages the card is not holding", async () => {
    // The loop runs over what is on screen, so with nothing on screen a press
    // reports work that did not happen. Same guard the local hide already had.
    chatUser.mockResolvedValue(card({ messages: [] }));
    draw("facebook");
    // Waits on the sentence rather than the button, for the same reason as
    // above: it only appears once the read has actually answered.
    await waitFor(() => expect(screen.getByText(/Nothing to hide here/)).toBeTruthy());
    expect(button(/Hide from viewers/).disabled).toBe(true);
    expect(button(/Show to viewers again/).disabled).toBe(true);
  });
});
