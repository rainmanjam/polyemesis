// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ChatMessageMenu } from "./ChatMessageMenu";
import type { ChatMessage } from "@/lib/types";

/* A REASON RENDERED WHERE NOBODY CAN REACH IT IS NOT A REASON.
 *
 * `modReason` went only into `title` on DropdownMenuItems, which carry
 * `data-[disabled]:pointer-events-none` (ui/dropdown-menu.tsx:36). An element
 * that receives no pointer events never fires the hover a native tooltip
 * needs, so the sentence was unreachable -- contradicting this file's own
 * header, which promises the reason beside every disabled action.
 */

const msg = (over: Partial<ChatMessage> = {}): ChatMessage =>
  ({
    id: "m1",
    platform: "twitch",
    text: "hello",
    at: 0,
    author: { id: "u1", name: "someone" },
    ...over,
  }) as ChatMessage;

const draw = (m: ChatMessage) =>
  render(
    <ChatMessageMenu
      message={m}
      anchor={{ x: 10, y: 10 }}
      onClose={() => {}}
      onOpenCard={() => {}}
    />,
  );

describe("ChatMessageMenu", () => {
  afterEach(cleanup);

  it("prints the reason when the message carries no user id to address", () => {
    // One of the two reasons an action is greyed, and the operator has to be
    // able to tell them apart: the platform has no such API, versus THIS
    // message arrived without an author id.
    draw(msg({ author: { id: "", name: "someone" } } as Partial<ChatMessage>));
    expect(screen.getByText(/sent no user id with this message/)).toBeTruthy();
  });

  it("prints the reason when the platform publishes no moderation API", () => {
    // A platform with no capability row falls back to "unverified", which
    // `canModerate` treats as no. Without this sentence the menu is five
    // greyed rows with no explanation, which reads as a broken tool.
    draw(msg({ platform: "custom" }));
    expect(screen.getByText(/publishes no moderation API/)).toBeTruthy();
  });

  it("says nothing extra when every action is available", () => {
    draw(msg({ platform: "twitch" }));
    expect(screen.queryByText(/publishes no moderation API/)).toBeNull();
    expect(screen.queryByText(/sent no user id/)).toBeNull();
  });
});
