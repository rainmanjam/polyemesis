// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { ChatMessage, ChatPlatform, ChatStatus } from "@/lib/types";

/* ChatPanel had ten of its hundred and fifty-six statements covered, and the
 * one it documents most carefully -- what an empty timeline SAYS -- was among
 * the uncovered ones.
 *
 * That matters more here than a percentage does. ChatEmpty exists because "no
 * messages" is true in five different situations and useful in none of them,
 * and the fifth was a real bug: the component was handed the FILTERED list and
 * knew nothing about the filter, so with every talking platform switched off it
 * said "Connected and waiting. Nothing has been said yet." over a scrollback
 * that was full. A moderator reading that sentence stops looking.
 *
 * The hooks are mocked because the pane's whole job is to render what
 * internal/chat states, and this file is about that rendering. What is NOT
 * mocked is the filter, the empty-state ranking or the delete path -- those are
 * the component. */

const remove = vi.fn();

interface FeedState {
  messages: ChatMessage[];
  statuses: ChatStatus[];
  limits: unknown[];
  configured: boolean;
  loading: boolean;
  connected: boolean;
  stored: boolean;
  error: string;
  frameError: boolean;
}

let feed: FeedState;

vi.mock("@/hooks/useChatFeed", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useChatFeed")>();
  return {
    ...actual,
    useChatFeed: () => ({ ...feed, reload: vi.fn(), remove }),
  };
});

vi.mock("@/hooks/useChatSearch", () => ({
  useChatSearch: () => ({
    query: "",
    setQuery: vi.fn(),
    active: false,
    results: [],
    loading: false,
    error: "",
    truncated: false,
    note: "",
    clear: vi.fn(),
  }),
}));

const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn(), message: vi.fn() },
}));

// Imported after the mocks so the component picks them up.
const { ChatPanel, ChatEmpty } = await import("./ChatPanel");

function message(id: string, platform: ChatPlatform, name: string, text: string): ChatMessage {
  return {
    id,
    platform,
    account: `${platform}-acct`,
    author: { id: `u-${name}`, name },
    text,
    at: "2026-01-01T10:00:00Z",
  };
}

function status(platform: ChatPlatform): ChatStatus {
  return {
    platform,
    account: `${platform}-acct`,
    state: "live",
    since: "2026-01-01T09:00:00Z",
    received: 1,
    sent: 0,
    restarts: 0,
    canSend: true,
  };
}

beforeEach(() => {
  // jsdom implements no scrolling at all, and the timeline pins itself to the
  // bottom on every new message. Not a product concern -- the assertion is
  // never about the scroll -- but without this every render throws.
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
  remove.mockReset();
  toastError.mockReset();
  feed = {
    messages: [],
    statuses: [],
    limits: [],
    configured: true,
    loading: false,
    connected: true,
    stored: false,
    error: "",
    frameError: false,
  };
});

afterEach(cleanup);

describe("the platform filter", () => {
  beforeEach(() => {
    feed.messages = [
      message("1", "twitch", "ana", "from twitch"),
      message("2", "youtube", "bo", "from youtube"),
    ];
    feed.statuses = [status("twitch"), status("youtube")];
  });

  it("hides one platform's messages and keeps the other's", () => {
    render(<ChatPanel showComposer={false} />);
    expect(screen.getByText("from twitch")).toBeTruthy();
    expect(screen.getByText("from youtube")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /twitch/i }));

    expect(screen.queryByText("from twitch")).toBeNull();
    expect(
      screen.getByText("from youtube"),
      "switching one platform off took the other's messages with it",
    ).toBeTruthy();
  });

  it("says the filter is the reason, rather than that nothing was said", () => {
    // THE BUG THIS PROP EXISTS FOR. With every talking platform switched off
    // the pane is empty, and the honest sentence names the filter. "Nothing has
    // been said yet" is false here, and false in the direction that makes a
    // moderator stop looking at a scrollback that is actually full.
    render(<ChatPanel showComposer={false} />);
    fireEvent.click(screen.getByRole("button", { name: /twitch/i }));
    fireEvent.click(screen.getByRole("button", { name: /youtube/i }));

    expect(screen.getByText(/filter is hiding 2 messages/i)).toBeTruthy();
    expect(screen.queryByText(/nothing has been said yet/i)).toBeNull();
  });

  it("puts a platform back", () => {
    render(<ChatPanel showComposer={false} />);
    const twitch = screen.getByRole("button", { name: /twitch/i });
    fireEvent.click(twitch);
    expect(screen.queryByText("from twitch")).toBeNull();
    fireEvent.click(twitch);
    expect(screen.getByText("from twitch")).toBeTruthy();
  });
});

describe("the header badges each answer a different question", () => {
  it("says history when the scrollback is stored rather than live", () => {
    feed.stored = true;
    render(<ChatPanel showComposer={false} />);
    expect(screen.getByText("history")).toBeTruthy();
    expect(screen.queryByText("offline")).toBeNull();
  });

  it("says offline only once chat is configured, not on an install without it", () => {
    // An unconfigured install is not "offline" -- there is nothing to be
    // offline from, and the empty state already explains it in a sentence.
    feed.configured = false;
    feed.connected = false;
    render(<ChatPanel showComposer={false} />);
    expect(screen.queryByText("offline")).toBeNull();

    cleanup();
    feed.configured = true;
    render(<ChatPanel showComposer={false} />);
    expect(screen.getByText("offline")).toBeTruthy();
  });

  it("says frame error separately, because a dropped frame leaves connected true", () => {
    // #13/#21: unlike a closed socket, a frame that failed to parse gives no
    // other signal at all. Without its own badge the timeline is silently
    // missing something while every other indicator reads healthy.
    feed.frameError = true;
    render(<ChatPanel showComposer={false} />);
    expect(screen.getByText("frame error")).toBeTruthy();
    expect(screen.queryByText("offline")).toBeNull();
  });
});

describe("a refused delete", () => {
  it("repeats the platform's own words rather than a generic failure", async () => {
    feed.messages = [message("1", "twitch", "ana", "from twitch")];
    feed.statuses = [status("twitch")];
    remove.mockRejectedValue(
      new Error("polyemesis cannot delete twitch messages; use the twitch dashboard"),
    );

    render(<ChatPanel showComposer={false} />);
    const del = screen.getAllByRole("button", { name: /delete/i })[0];
    fireEvent.click(del);

    /* THE CONFIRMATION IS NOW BETWEEN THE CLICK AND THE DELETE, and this test
       drove the old one-click path. #770: this panel is the chat page's menu
       mounted in the dashboard pane, and it deleted without asking while the
       identical menu on ChatPage confirmed.

       Confirming here rather than deleting the step: what this test is about is
       that a REFUSAL repeats the platform's own words, and that is still worth
       pinning — it just now happens one gesture later. */
    const confirm = await screen.findByRole("button", { name: "Delete for everyone" });
    fireEvent.click(confirm);

    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(
      String(toastError.mock.calls[0][0]),
      "the moderator was told 'could not delete' when the platform had said exactly what to do instead",
    ).toContain("twitch dashboard");
  });
});

describe("what an empty timeline says", () => {
  const base = { loading: false, configured: true, error: "", statuses: [] as ChatStatus[] };

  it("ranks loading above everything", () => {
    render(<ChatEmpty {...base} loading error="something broke" />);
    expect(screen.getByText(/loading chat/i)).toBeTruthy();
  });

  it("shows the error over the configuration advice", () => {
    render(<ChatEmpty {...base} error="the socket refused the token" />);
    expect(screen.getByText("the socket refused the token")).toBeTruthy();
  });

  it("tells an unconfigured install where to go, and that a restart is needed", () => {
    render(<ChatEmpty {...base} configured={false} />);
    const text = screen.getByText(/not running on this server/i).textContent ?? "";
    expect(text).toMatch(/Settings/);
    expect(text, "the operator was not told a restart is part of it").toMatch(/restart/i);
  });

  it("distinguishes running-with-nothing-attached from waiting-for-a-message", () => {
    render(<ChatEmpty {...base} />);
    expect(screen.getByText(/no platform account is attached yet/i)).toBeTruthy();

    cleanup();
    render(<ChatEmpty {...base} statuses={[status("twitch")]} />);
    expect(screen.getByText(/nothing has been said yet/i)).toBeTruthy();
  });

  it("counts one hidden message in the singular", () => {
    // A sentence that says "1 messages" is the kind of thing that makes a
    // reader trust the rest of the pane less.
    render(<ChatEmpty {...base} statuses={[status("twitch")]} hiddenMessages={1} />);
    expect(screen.getByText(/hiding 1 message\./i)).toBeTruthy();
  });

  it("ranks the filter above 'nothing has been said'", () => {
    render(<ChatEmpty {...base} statuses={[status("twitch")]} hiddenMessages={4} />);
    expect(screen.getByText(/hiding 4 messages/i)).toBeTruthy();
    expect(screen.queryByText(/nothing has been said yet/i)).toBeNull();
  });
});

/* THE ROW TRASH ICON MUST ASK THE PLATFORM, NOT THE PROP.
 *
 * `{onDelete && …}` says the operator may delete. It says nothing about
 * whether the platform CAN, and the context menu was fixed for exactly that
 * while this icon kept the ungated form: the line rendered a live trash can,
 * and adding the confirmation dialog made it worse — the operator now got a
 * dialog promising removal, clicked through it, and then met a server error.
 *
 * Two specimens, because there are two ways to be unable to delete and the
 * dangerous one is invisible to the compiler:
 *
 *   rumble  is OUTSIDE the ChatPlatform union (types.ts) and still arrives at
 *           runtime. ChatMessageMenu.reason.test.tsx says why that matters --
 *           "the type system believes this message cannot exist" -- and it is
 *           exactly how an ungated Delete survived review the first time.
 *   trovo   is INSIDE the union with no row in the adapter table.
 *
 * Twitch is the control: if the gate were wired backwards, or to a constant,
 * one of these three would say so.
 */
describe("the row delete icon is gated on what the platform can do", () => {
  const trash = (name: string) =>
    screen.getByLabelText(`Delete message from ${name}`) as HTMLButtonElement;

  beforeEach(() => {
    feed.messages = [
      // The cast is the point, not a workaround: this platform reaches the
      // renderer and the union says it cannot.
      message("1", "rumble" as unknown as ChatPlatform, "rumbler", "no moderation API at all"),
      message("2", "trovo", "trovan", "in the union, absent from the adapter table"),
      message("3", "twitch", "twitcher", "can be deleted upstream"),
    ];
    feed.statuses = [status("trovo"), status("twitch")];
  });

  it("disables it on a platform outside the union, and says why", () => {
    render(<ChatPanel />);
    const btn = trash("rumbler");
    expect(btn.disabled).toBe(true);
    expect(btn.title).toMatch(/Rumble/i);
    expect(btn.title).toMatch(/no moderation API/i);
  });

  it("disables it on an in-union platform with no adapter row", () => {
    render(<ChatPanel />);
    const btn = trash("trovan");
    expect(btn.disabled).toBe(true);
    // The blanket reason, not the per-action one: every adapter in the table
    // can delete, so the only reachable "cannot" is having no adapter at all.
    // That is what Trovo is, and the sentence has to name it -- a disabled
    // control with no explanation sends a moderator looking for their own
    // mistake instead of reading that the platform publishes nothing to call.
    expect(btn.title).toMatch(/Trovo/i);
    expect(btn.title).toMatch(/no moderation API/i);
  });

  /* POSITIVE CONTROL. A gate wired to `false`, or to the wrong action, would
   * pass the assertion above and fail this one. */
  it("leaves it live on a platform that can delete", () => {
    render(<ChatPanel />);
    const btn = trash("twitcher");
    expect(btn.disabled).toBe(false);
    expect(btn.title).toMatch(/Delete on the platform/i);
  });
});
