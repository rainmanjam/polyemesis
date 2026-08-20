// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import type { ChatUserCard as CardData } from "@/lib/types";

/* WHAT THIS CARD IS ALLOWED TO SAY ABOUT SOMEBODY'S HISTORY.
 *
 * A moderator opens it to decide whether one bad line was a bad moment or a
 * pattern, and it answers with a count and a sentence. Both were read off one
 * value that a FAILED READ and a SUCCESSFUL EMPTY ONE both produced.
 *
 * jsdom because Radix renders the dialog through a portal.
 */

const chatUser = vi.spyOn(api, "chatUser");

function draw() {
  return render(
    <ChatUserCard
      platform="twitch"
      authorId="u1"
      authorName="someone"
      open
      onOpenChange={() => {}}
    />,
  );
}

const card = (over: Partial<CardData> = {}): CardData =>
  ({ name: "someone", messages: [], ...over }) as CardData;

describe("A4-4: a scrollback read that failed", () => {
  beforeEach(() => {
    chatUser.mockReset();
  });
  afterEach(cleanup);

  it("does not report a failed read as an empty history", async () => {
    chatUser.mockRejectedValue(new Error("read failed"));
    draw();
    // The exonerating sentence is the whole hazard: a moderator reads
    // "Nothing from this person is in the scrollback" as a first offence.
    await waitFor(() =>
      expect(screen.queryByText(/Nothing from this person is in the scrollback/)).toBeNull(),
    );
    expect(screen.getAllByText(/could not be read/).length).toBeGreaterThan(0);
    // And the count must not be stated either -- "0 messages on record" is the
    // same false claim in a number.
    expect(screen.queryByText(/0 messages on record/)).toBeNull();
  });

  it("still says the history is empty when the server actually answered that", async () => {
    chatUser.mockResolvedValue(card({ messages: [] }));
    draw();
    await waitFor(() =>
      expect(screen.getByText(/Nothing from this person is in the scrollback/)).toBeTruthy(),
    );
    expect(screen.getByText(/0 messages on record/)).toBeTruthy();
  });
});

describe("A4-7: hiding messages the card is not holding", () => {
  beforeEach(() => {
    chatUser.mockReset();
  });
  afterEach(cleanup);

  const hideButton = () => screen.getByRole("button", { name: /Hide here only/ });

  it("cannot be pressed when there is nothing to hide", async () => {
    // The handler loops over `messages`. With none it hid nothing and still
    // toasted "Hidden in polyemesis only", describing work that did not happen.
    chatUser.mockResolvedValue(card({ messages: [] }));
    draw();
    await waitFor(() => expect((hideButton() as HTMLButtonElement).disabled).toBe(true));
  });

  it("says why, rather than being a grey button with no reason", async () => {
    chatUser.mockResolvedValue(card({ messages: [] }));
    draw();
    await waitFor(() => expect(screen.getByText(/Nothing to hide here/)).toBeTruthy());
  });

  it("is live once there is at least one message it is actually holding", async () => {
    chatUser.mockResolvedValue(
      card({
        messages: [
          { id: "m1", text: "hi", at: "2026-08-20T12:00:00Z", platform: "twitch", author: { id: "u1", name: "someone" } },
        ],
      } as Partial<CardData>),
    );
    draw();
    await waitFor(() => expect((hideButton() as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByText(/Nothing to hide here/)).toBeNull();
  });
});
