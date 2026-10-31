// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ChatEmpty } from "./ChatPanel";
import type { ChatStatus } from "@/lib/types";

/* "CONNECTED AND WAITING. NOTHING HAS BEEN SAID YET." OVER A FULL SCROLLBACK.
 *
 * ChatEmpty was handed the FILTERED list and knew nothing about the filter, so
 * with every talking platform switched off it claimed silence. The dimmed chip
 * is on screen, so it is recoverable -- but the sentence is false in the
 * direction that makes a moderator stop looking.
 */

const statuses = [{ platform: "twitch", connected: true }] as unknown as ChatStatus[];

const draw = (hiddenMessages?: number) =>
  render(
    <ChatEmpty
      loading={false}
      configured
      error=""
      statuses={statuses}
      hiddenMessages={hiddenMessages}
    />,
  );

describe("ChatEmpty", () => {
  afterEach(cleanup);

  it("does not claim silence when the filter is what is silent", () => {
    draw(12);
    expect(screen.queryByText(/Nothing has been said yet/)).toBeNull();
    expect(screen.getByText(/filter is hiding 12 messages/)).toBeTruthy();
  });

  it("says one message rather than 1 messages", () => {
    draw(1);
    expect(screen.getByText(/hiding 1 message\./)).toBeTruthy();
  });

  it("still claims silence when the pane really is empty", () => {
    draw(0);
    expect(screen.getByText(/Connected and waiting\. Nothing has been said yet\./)).toBeTruthy();
  });

  it("leaves the four earlier situations alone — an error still wins", () => {
    render(
      <ChatEmpty
        loading={false}
        configured
        error="chat socket refused"
        statuses={statuses}
        hiddenMessages={5}
      />,
    );
    expect(screen.getByText("chat socket refused")).toBeTruthy();
  });
});

/* ONE EMPTY STATE (app-council, EmptyState adoption on Chat).
 *
 * The timeline's empty pane was an 11px grey sentence, below the 12px floor and
 * unlike every other empty list. It is the kit's EmptyState now: a short title
 * saying what is missing, and the existing sentence as the body. */
describe("ChatEmpty uses the kit's empty state", () => {
  afterEach(cleanup);

  it("titles the waiting pane and keeps the sentence as its body", () => {
    draw(0);
    expect(screen.getByText("No messages yet")).toBeTruthy();
    expect(screen.getByText(/Connected and waiting\./)).toBeTruthy();
  });

  it("titles a filtered pane as filtered, not as silent", () => {
    draw(3);
    expect(screen.getByText("Messages are filtered out")).toBeTruthy();
  });

  it("titles an error as an error", () => {
    render(<ChatEmpty loading={false} configured error="chat socket refused" statuses={statuses} />);
    expect(screen.getByText("Chat could not load")).toBeTruthy();
  });
});
