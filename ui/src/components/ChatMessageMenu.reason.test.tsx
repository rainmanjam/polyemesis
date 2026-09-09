// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ChatMessageMenu } from "./ChatMessageMenu";
import { moderationColumnSaysYes } from "@/lib/chatModeration";
import type { Asks } from "@/hooks/useConfirm";
import type { ChatMessage, ChatPlatform } from "@/lib/types";

/* A REASON RENDERED WHERE NOBODY CAN REACH IT IS NOT A REASON.
 *
 * `modReason` went only into `title` on DropdownMenuItems, which carry
 * `data-[disabled]:pointer-events-none` (ui/dropdown-menu.tsx:36). An element
 * that receives no pointer events never fires the hover a native tooltip
 * needs, so the sentence was unreachable -- contradicting this file's own
 * header, which promises the reason beside every disabled action.
 *
 * And a reason attached to the WRONG SET OF ITEMS is not much better, which is
 * the second half of this file. Everything here was gated on one capability
 * COLUMN -- "can polyemesis moderate this platform" -- so Facebook, which
 * answers yes because it deletes and hides comments and implements no
 * chat.Banner at all, rendered five live moderation controls that hub.go
 * refused on every press.
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

/** A stand-in for `useConfirm().ask`.
 *
 *  The brand on `Asks` is phantom, so minting one takes a cast. That is the
 *  point rather than a hole: a test opting in deliberately is fine, and what
 *  the brand prevents is PRODUCTION code passing the immediate deleter to a
 *  prop that promises to ask first, which now cannot be written by accident. */
const asks = (fn: (m: ChatMessage) => void = () => {}) => fn as Asks<ChatMessage>;

const draw = (m: ChatMessage, onDelete?: Asks<ChatMessage>) =>
  render(
    <ChatMessageMenu
      message={m}
      anchor={{ x: 10, y: 10 }}
      onClose={() => {}}
      onOpenCard={() => {}}
      onDelete={onDelete}
    />,
  );

/** A menu item, and whether it is inert.
 *
 *  Radix marks a disabled item with aria-disabled rather than the `disabled`
 *  attribute, because the item is a div and not a button — so `.disabled` is
 *  undefined on it and reading that instead would report every item as live. */
const item = (name: RegExp) => screen.getByRole("menuitem", { name });
const inert = (name: RegExp) => item(name).getAttribute("aria-disabled") === "true";

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
    // A platform with no capability row falls back to "unverified", and no
    // chat adapter implements anything for it. Without this sentence the menu
    // is five greyed rows with no explanation, which reads as a broken tool.
    draw(msg({ platform: "custom" }));
    expect(screen.getByText(/publishes no moderation API/)).toBeTruthy();
  });

  it("says nothing extra when every action is available", () => {
    draw(msg({ platform: "twitch" }));
    expect(screen.queryByText(/publishes no moderation API/)).toBeNull();
    expect(screen.queryByText(/sent no user id/)).toBeNull();
  });
});

/* #769: the column says yes and the action says no.
 *
 * The fixture the guard was missing. Facebook is not an edge case invented for
 * a test — it is the one platform in the matrix whose summary and whose
 * adapter genuinely disagree, and every control in this menu was reading the
 * summary.
 */
describe("ChatMessageMenu on a platform whose moderation column says yes", () => {
  afterEach(cleanup);

  it("is a platform whose column really does say yes", () => {
    // Pins the premise. If Facebook's column ever changes to "no" this whole
    // describe stops testing what it claims to, and would keep passing.
    expect(moderationColumnSaysYes("facebook")).toBe(true);
  });

  it("renders the timeouts inert rather than enabled and always failing", () => {
    draw(msg({ platform: "facebook" }));
    // Hub.Ban type-asserts chat.Banner and FacebookAdapter has no Ban, so
    // every one of these was a press that could only produce an error toast.
    expect(inert(/Time out 1 min/)).toBe(true);
    expect(inert(/Time out 1 day/)).toBe(true);
  });

  it("renders the permanent ban inert too", () => {
    draw(msg({ platform: "facebook" }));
    expect(inert(/Ban permanently/)).toBe(true);
  });

  it("says WHY, in a sentence naming what to do instead", () => {
    // A disabled control with no explanation is only marginally better than a
    // broken one: the moderator still cannot tell an unsupported platform from
    // a broken tool, and still has nowhere to go.
    draw(msg({ platform: "facebook" }));
    expect(screen.getByText(/no chat ban API/)).toBeTruthy();
    expect(screen.getByText(/Facebook page itself/)).toBeTruthy();
  });

  it("leaves Delete live, because Facebook can genuinely delete a comment", () => {
    // The other half of per-action gating, and the reason a menu-wide banner
    // is no longer the right shape: greying Delete here would be the same
    // defect pointed the other way.
    draw(msg({ platform: "facebook" }), asks());
    expect(inert(/Delete message/)).toBe(false);
  });

  it("still offers Delete on a comment that arrived with no author id", () => {
    // Delete addresses a MESSAGE. Folding it in with the person-actions is
    // what made a missing author id disable it for no reason.
    draw(
      msg({ platform: "facebook", author: { id: "", name: "someone" } } as Partial<ChatMessage>),
      asks(),
    );
    expect(inert(/Delete message/)).toBe(false);
    expect(inert(/Time out 1 min/)).toBe(true);
  });
});

/* #769, the mirror image: an action rendered with no gate at all. */
describe("ChatMessageMenu on a platform that implements nothing", () => {
  afterEach(cleanup);

  // Rumble is outside the ChatPlatform union on purpose (types.ts:234) and
  // still arrives at runtime, which is exactly how an ungated Delete survived
  // review: the type system believes this message cannot exist.
  const rumble = () =>
    msg({ platform: "rumble" as unknown as ChatPlatform, author: { id: "u1", name: "someone" } });

  it("renders Delete inert instead of live-and-refused", () => {
    // It was `{onDelete && <DropdownMenuItem …>}` with no gate whatever, three
    // lines under this menu's own sentence saying Rumble publishes no
    // moderation API. Pressing it reached hub.go:699, which refuses.
    draw(rumble(), asks());
    expect(inert(/Delete message/)).toBe(true);
  });

  it("says why, on screen, in the same menu", () => {
    draw(rumble(), asks());
    expect(screen.getByText(/Rumble publishes no moderation API/)).toBeTruthy();
  });

  it("names the platform properly rather than echoing the lowercase id", () => {
    // platformNoun passes an unknown platform straight through, which is how
    // the menu came to print "rumble publishes no moderation API".
    draw(rumble(), asks());
    expect(screen.queryByText(/^rumble /)).toBeNull();
  });

  it("does not repeat one sentence once per greyed item", () => {
    // Everything in this menu is inert for the same reason, so it is said
    // once. Five copies is how a two-second shortcut turns back into a form.
    draw(rumble(), asks());
    expect(screen.getAllByText(/publishes no moderation API/)).toHaveLength(1);
  });
});
